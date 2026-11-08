package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type YahooClient struct {
	root string
}

func NewYahooClient(root string) *YahooClient {
	return &YahooClient{root: root}
}

func (p *YahooClient) Fetch(ctx context.Context, ticker string) (Snapshot, error) {
	var response yahooResponse
	err := p.request(ctx, "snapshot", ticker, &response)
	return response.Snapshot, err
}

func (p *YahooClient) RealizedVolatility(ctx context.Context, ticker string) (map[int]float64, error) {
	var response yahooResponse
	err := p.request(ctx, "rv", ticker, &response)
	return response.Values, err
}

type yahooResponse struct {
	Snapshot
	Values      map[int]float64 `json:"values"`
	Error       string          `json:"error"`
	RateLimited bool            `json:"rate_limited"`
}

func (p *YahooClient) request(ctx context.Context, op, ticker string, out *yahooResponse) error {
	python := ""
	for _, path := range []string{
		filepath.Join(p.root, "python", "bin", "python3"),
		filepath.Join(p.root, ".venv", "bin", "python"),
	} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			python = path
			break
		}
	}
	if python == "" {
		return fmt.Errorf("Yahoo 备用源需要安装包内的 Python 或源码目录的 .venv")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", "-B", filepath.Join(p.root, "app", "yahoo_cli.py"), "--op="+op, "--ticker="+ticker)
	// The subprocess receives no TideDesk database configuration.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GEX_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Yahoo %s: %w %s", op, err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("Yahoo %s 返回无效 JSON: %w", op, err)
	}
	if out.RateLimited {
		return &ProviderError{Status: 429, RetryAfter: 30 * time.Minute, Message: out.Error}
	}
	if out.Error != "" {
		return fmt.Errorf("Yahoo %s: %s", op, out.Error)
	}
	return nil
}
