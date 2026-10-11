// Command record captures one full traffic cycle of the running booth
// dashboard (headless Chrome, 1920x1080) and renders a time-lapse MP4 and GIF
// for the README. It records the real dashboard, k9s panel included.
//
//	go run ./tools/record                     # from the repo root, with `make up` running
//	go run ./tools/record -rollout=false -speed 8
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type directorState struct {
	Director *struct {
		Phase        string  `json:"phase"`
		Elapsed      float64 `json:"elapsed"`
		CycleSeconds int     `json:"cycleSeconds"`
		Cycle        int     `json:"cycle"`
		LastRollout  *struct {
			Cycle int `json:"cycle"`
		} `json:"lastRollout"`
	} `json:"director"`
}

func main() {
	base := flag.String("url", "http://127.0.0.1:8080", "dashboard base URL")
	out := flag.String("out", "docs", "output directory for demo.mp4 / demo.gif")
	speed := flag.Int("speed", 10, "time-lapse speed-up")
	fps := flag.Int("fps", 20, "output video frame rate")
	rollout := flag.Bool("rollout", true, "roll out a new worker version at the start of the cycle")
	extra := flag.Duration("tail", 15*time.Second, "keep recording this long after the cycle ends")
	maxDur := flag.Duration("max", 0, "stop after this long (0 = one full cycle); for quick tests")
	noWait := flag.Bool("nowait", false, "start immediately instead of on a cycle boundary")
	keep := flag.String("frames", "", "keep captured frames in this directory (default: temp, deleted)")
	flag.Parse()

	interval := time.Second * time.Duration(*speed) / time.Duration(*fps)
	frames := *keep
	if frames == "" {
		tmp, err := os.MkdirTemp("", "demo-frames-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(tmp)
		frames = tmp
	} else if err := os.MkdirAll(frames, 0o755); err != nil {
		log.Fatal(err)
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.WindowSize(1920, 1080),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("force-device-scale-factor", "1"),
	)
	actx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()

	badge := fmt.Sprintf(`(() => {
		const b = document.createElement('div');
		b.textContent = '▶ %d× time-lapse · one full traffic cycle';
		b.style.cssText = 'position:fixed;left:50%%;top:3.4rem;transform:translateX(-50%%);z-index:99;' +
		  'font:500 13px "Noto Sans Mono",monospace;color:#141414;background:#f8fafc;padding:.3rem .7rem;';
		document.body.appendChild(b);
	})()`, *speed)
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(1920, 1080),
		chromedp.Navigate(*base+"/?k9s=1&kiosk=1"),
		chromedp.Sleep(6*time.Second), // fonts, history, k9s session
	); err != nil {
		log.Fatal(err)
	}
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[any](badge)); err != nil {
		log.Fatal(err)
	}

	// Start on a cycle boundary so the recording is one complete cycle.
	cycle, autoRollout := 300, false
	if !*noWait {
		log.Println("page ready; waiting for the start of the next traffic cycle…")
		cycle, autoRollout = waitForCycleStart(*base)
	}
	if *rollout && autoRollout {
		log.Println("the director is already rolling out a new version this cycle")
	} else if *rollout {
		if _, err := http.Post(*base+"/api/rollout", "application/json", nil); err != nil {
			log.Fatal(err)
		}
		log.Println("rollout requested")
	}

	total := time.Duration(cycle)*time.Second + *extra
	if *maxDur > 0 {
		total = *maxDur
	}
	log.Printf("recording %s at one frame every %s…", total, interval)
	start := time.Now()
	n := 0
	for time.Since(start) < total {
		next := start.Add(time.Duration(n+1) * interval)
		buf, err := chromedp.Run(ctx, jpegScreenshot(88))
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(frames, fmt.Sprintf("%05d.jpg", n)), buf, 0o644); err != nil {
			log.Fatal(err)
		}
		n++
		if n%60 == 0 {
			log.Printf("  %d frames (%s)", n, time.Since(start).Round(time.Second))
		}
		time.Sleep(time.Until(next))
	}
	log.Printf("captured %d frames; encoding…", n)

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	in := filepath.Join(frames, "%05d.jpg")
	rate := fmt.Sprint(*fps)
	run("ffmpeg", "-y", "-loglevel", "error", "-framerate", rate, "-i", in,
		"-vf", "scale=1600:-2:flags=lanczos", "-c:v", "libx264", "-preset", "slow", "-crf", "24",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart", filepath.Join(*out, "demo.mp4"))
	run("ffmpeg", "-y", "-loglevel", "error", "-framerate", rate, "-i", in,
		// Tuned for the README: ~9 MB for a full cycle at 800px.
		"-vf", "fps=6,scale=800:-2:flags=lanczos,split[a][b];[a]palettegen=max_colors=80:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle",
		"-loop", "0", filepath.Join(*out, "demo.gif"))
	for _, f := range []string{"demo.mp4", "demo.gif"} {
		if st, err := os.Stat(filepath.Join(*out, f)); err == nil {
			log.Printf("wrote %s (%.1f MB)", filepath.Join(*out, f), float64(st.Size())/1e6)
		}
	}
}

// waitForCycleStart blocks until the director is at the very start of a cycle
// and returns the cycle length in seconds, and whether the director itself is
// rolling out a new version this cycle.
func waitForCycleStart(base string) (int, bool) {
	for {
		var s directorState
		if resp, err := http.Get(base + "/api/snapshot"); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&s)
			resp.Body.Close()
		}
		if d := s.Director; d != nil && d.Phase == "idle" && d.Elapsed <= 2 {
			time.Sleep(time.Second) // let the director's own rollout (if due) register
			var after directorState
			if resp, err := http.Get(base + "/api/snapshot"); err == nil {
				_ = json.NewDecoder(resp.Body).Decode(&after)
				resp.Body.Close()
			}
			auto := after.Director != nil && after.Director.LastRollout != nil && after.Director.LastRollout.Cycle == d.Cycle
			return d.CycleSeconds, auto
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// jpegScreenshot captures the viewport as JPEG (much smaller than PNG frames).
func jpegScreenshot(quality int64) chromedp.Action[[]byte] {
	return func(ctx context.Context, t *chromedp.Target) ([]byte, error) {
		r, err := cdp.Call(ctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format:  page.CaptureScreenshotFormatJpeg,
			Quality: &quality,
		})
		if err != nil {
			return nil, err
		}
		return r.Data, nil
	}
}

func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("%s: %v", name, err)
	}
}
