package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WhoamiTimeout bounds the mailbox lookup at session start.
const WhoamiTimeout = 2 * time.Second

// Token finds the mailbox bearer token: MAILBOX_TOKEN in the environment, or
// else the line that exports it in the user's zsh secrets file. Hooks may run
// without the user's shell environment, which is why the file is read at all.
//
// The token is returned to the caller and never logged or stored.
func Token(getenv func(string) string) string {
	if t := strings.TrimSpace(getenv("MAILBOX_TOKEN")); t != "" {
		return t
	}
	data, err := os.ReadFile(SecretsPath(getenv))
	if err != nil {
		return ""
	}
	return parseToken(data)
}

// SecretsPath is ${ZDOTDIR:-$HOME/.zsh}/secrets.zsh.
func SecretsPath(getenv func(string) string) string {
	dir := getenv("ZDOTDIR")
	if dir == "" {
		dir = filepath.Join(getenv("HOME"), ".zsh")
	}
	return filepath.Join(dir, "secrets.zsh")
}

// parseToken reads MAILBOX_TOKEN from shell source. It accepts the forms an
// assignment is written in by hand: with or without export, and single,
// double, or no quotes. The last assignment wins, as it would in the shell.
func parseToken(data []byte) string {
	token := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "export ")
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "MAILBOX_TOKEN=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch {
		case len(value) >= 2 && (value[0] == '\'' || value[0] == '"'):
			if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
				value = value[1 : end+1]
			} else {
				value = ""
			}
		default:
			value, _, _ = strings.Cut(value, " ")
			value, _, _ = strings.Cut(value, "#")
		}
		if value != "" {
			token = value
		}
	}
	return token
}

// Whoami asks the mailbox server for the address that reaches sessionID. A
// nil client uses http.DefaultClient; the call is bounded by WhoamiTimeout
// whatever the client's own settings.
func Whoami(ctx context.Context, client *http.Client, baseURL, token, sessionID string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, WhoamiTimeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/v1/whoami"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Mailbox-Session", sessionID)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %s", url, resp.Status)
	}
	var answer struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	if answer.Address == "" {
		return "", errors.New(url + " returned no address")
	}
	return answer.Address, nil
}
