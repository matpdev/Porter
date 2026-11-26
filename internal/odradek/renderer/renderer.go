package renderer

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"codeberg.org/matheus/porter/internal/agents"
	"github.com/mxschmitt/playwright-go"
)

//go:embed assets/*
var assetsFS embed.FS

// Renderer renders code snippets and Mermaid diagrams into 2x high-DPI PNGs via local headless Chromium.
type Renderer struct {
	mediaDir string

	mu      sync.Mutex
	pw      *playwright.Playwright
	browser playwright.Browser
	srv     *http.Server
	baseURL string
}

func NewRenderer(mediaDir string) *Renderer {
	return &Renderer{mediaDir: mediaDir}
}

func (r *Renderer) ensureStarted() error {
	if r.browser != nil {
		return nil
	}
	if err := os.MkdirAll(r.mediaDir, 0o755); err != nil {
		return fmt.Errorf("create media dir: %w", err)
	}

	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen asset server: %w", err)
	}
	r.srv = &http.Server{Handler: mux}
	go func() { _ = r.srv.Serve(ln) }()
	r.baseURL = "http://" + ln.Addr().String()

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("start playwright: %w", err)
	}
	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	}
	if execPath := detectSystemChromium(); execPath != "" {
		launchOpts.ExecutablePath = playwright.String(execPath)
	}
	browser, err := pw.Chromium.Launch(launchOpts)
	if err != nil {
		_ = pw.Stop()
		return fmt.Errorf("launch chromium: %w", err)
	}

	r.pw = pw
	r.browser = browser
	return nil
}

func (r *Renderer) Render(ctx context.Context, postID string, media agents.PostMedia) (string, error) {
	if media.Type == agents.MediaNone || media.Type == "" {
		return "", nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.ensureStarted(); err != nil {
		return "", err
	}

	page, err := r.browser.NewPage(playwright.BrowserNewPageOptions{
		Viewport:          &playwright.Size{Width: 1280, Height: 900},
		DeviceScaleFactor: playwright.Float(2),
	})
	if err != nil {
		return "", fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	var targetURL, fnName string
	switch media.Type {
	case agents.MediaCodeSnippet:
		targetURL = r.baseURL + "/assets/code-card.html"
		fnName = "window.renderCard"
	case agents.MediaMermaid:
		targetURL = r.baseURL + "/assets/mermaid-card.html"
		fnName = "window.renderDiagram"
	default:
		return "", fmt.Errorf("unsupported media type %q", media.Type)
	}

	if _, err := page.Goto(targetURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return "", fmt.Errorf("goto %s: %w", targetURL, err)
	}

	payload := map[string]any{
		"title":    media.Title,
		"language": media.Language,
		"content":  media.Content,
	}
	if _, err := page.Evaluate(fmt.Sprintf("p => %s(p)", fnName), payload); err != nil {
		return "", fmt.Errorf("evaluate %s: %w", fnName, err)
	}

	if _, err := page.WaitForFunction("() => window.__RENDER_READY === true", nil); err != nil {
		return "", fmt.Errorf("wait render ready: %w", err)
	}
	if renderErr, err := page.Evaluate("() => window.__RENDER_ERROR || ''", nil); err == nil {
		if msg, ok := renderErr.(string); ok && msg != "" {
			return "", fmt.Errorf("template render error: %s", msg)
		}
	}

	outPath := filepath.Join(r.mediaDir, postID+".png")
	card := page.Locator("#card")
	if _, err := card.Screenshot(playwright.LocatorScreenshotOptions{
		Path:           playwright.String(outPath),
		OmitBackground: playwright.Bool(true),
	}); err != nil {
		return "", fmt.Errorf("screenshot #card: %w", err)
	}
	return outPath, nil
}

func (r *Renderer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.browser != nil {
		_ = r.browser.Close()
		r.browser = nil
	}
	if r.pw != nil {
		_ = r.pw.Stop()
		r.pw = nil
	}
	if r.srv != nil {
		_ = r.srv.Close()
		r.srv = nil
	}
}

func detectSystemChromium() string {
	if p := os.Getenv("PORTER_CHROMIUM_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, candidate := range []string{"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}
