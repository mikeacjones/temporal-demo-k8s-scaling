package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"k8s.io/client-go/rest"

	"github.com/mikeacjones/temporal-k8s-scaling-demo/web"
)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// inClusterK9s prepares k9s to run inside the dashboard pod, isolated from
// any developer kubeconfig: it sees exactly what the pod's service account
// (read-only, demo namespace) can see. Returns the command and environment.
func inClusterK9s(namespace, workDir string) ([]string, []string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, err
	}
	bin, err := exec.LookPath("k9s")
	if err != nil {
		return nil, nil, err
	}

	// k9s needs a writable config dir; seed it with the booth config + skin.
	cfgDir := filepath.Join(workDir, "k9s")
	if err := fs.WalkDir(web.K9s, "k9s", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(workDir, path)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := web.K9s.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}); err != nil {
		return nil, nil, fmt.Errorf("write k9s config: %w", err)
	}

	// A kubeconfig for the pod's own identity. tokenFile (not an inlined
	// token) so client-go picks up projected-token rotation.
	kubeconfig := filepath.Join(workDir, "kubeconfig")
	kc := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: demo
  cluster:
    server: %s
    certificate-authority: %s/ca.crt
users:
- name: dashboard
  user:
    tokenFile: %s/token
contexts:
- name: demo
  context:
    cluster: demo
    user: dashboard
    namespace: %s
current-context: demo
`, cfg.Host, saDir, saDir, namespace)
	if err := os.WriteFile(kubeconfig, []byte(kc), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write kubeconfig: %w", err)
	}

	cmd := []string{bin, "--context", "demo", "-n", namespace, "-c", "pods",
		"--readonly", "--headless", "--logoless", "--crumbsless", "--splashless", "--refresh", "1"}
	env := []string{
		"K9S_CONFIG_DIR=" + cfgDir,
		"KUBECONFIG=" + kubeconfig,
		"HOME=" + workDir,
		"XDG_STATE_HOME=" + workDir,
		"XDG_CACHE_HOME=" + workDir,
		"XDG_DATA_HOME=" + workDir,
	}
	return cmd, env, nil
}

// runK9s replaces this process with the isolated in-cluster k9s.
func runK9s() {
	ns := os.Getenv("K8S_NAMESPACE")
	if ns == "" {
		ns = "demo"
	}
	work, err := os.MkdirTemp(os.Getenv("WORK_DIR"), "k9s-exec-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "k9s:", err)
		os.Exit(1)
	}
	cmd, env, err := inClusterK9s(ns, work)
	if err != nil {
		fmt.Fprintln(os.Stderr, "k9s is only available inside the dashboard pod:", err)
		os.Exit(1)
	}
	if err := syscall.Exec(cmd[0], cmd, append(os.Environ(), env...)); err != nil {
		fmt.Fprintln(os.Stderr, "exec k9s:", err)
		os.Exit(1)
	}
}
