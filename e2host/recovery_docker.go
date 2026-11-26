//go:build linux

package e2host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

var projectRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type dockerMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Destination string `json:"Destination"`
}
type dockerContainer struct {
	ID     string            `json:"Id"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Mounts []dockerMount     `json:"Mounts"`
}
type dockerInspect struct {
	ID     string        `json:"Id"`
	Mounts []dockerMount `json:"Mounts"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

// VerifyDockerTermination reads the daemon's live container inventory. The
// operator container must itself belong to this Compose project and mount its
// exact Host and IPC volumes. Every other sender in that installation must be
// stopped; a bare assertion or a service restart is not termination evidence.
func VerifyDockerTermination(ctx context.Context, socket, project, selfID string) (string, error) {
	if ctx == nil || ctx.Err() != nil || socket == "" || !projectRef.MatchString(project) ||
		len(selfID) < 12 || len(selfID) > 64 {
		return "", ErrUnavailable
	}
	for _, ch := range selfID {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return "", ErrUnavailable
		}
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	read := func(path string, target any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
		if err != nil {
			return ErrUnavailable
		}
		resp, err := client.Do(req)
		if err != nil {
			return ErrUnavailable
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ErrUnavailable
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil || len(body) == 2<<20 || json.Unmarshal(body, target) != nil {
			return ErrUnavailable
		}
		return nil
	}
	var self dockerInspect
	if read("/containers/"+url.PathEscape(selfID)+"/json", &self) != nil ||
		!strings.HasPrefix(self.ID, selfID) || !self.State.Running ||
		self.Config.Labels["com.docker.compose.project"] != project ||
		self.Config.Labels["com.docker.compose.service"] != "e2-recovery" {
		return "", ErrUnavailable
	}
	required := map[string]string{
		"/h":           project + "_e2-host-state",
		"/run/e2-host": project + "_e2-host-ipc",
	}
	for _, mount := range self.Mounts {
		if want, ok := required[mount.Destination]; ok && mount.Type == "volume" && mount.Name == want {
			delete(required, mount.Destination)
		}
	}
	if len(required) != 0 {
		return "", ErrUnavailable
	}
	var containers []dockerContainer
	if read("/containers/json?all=1", &containers) != nil || len(containers) == 0 || len(containers) > 1024 {
		return "", ErrUnavailable
	}
	slices.SortFunc(containers, func(a, b dockerContainer) int { return strings.Compare(a.ID, b.ID) })
	foundSelf, foundPrep := false, false
	for _, c := range containers {
		if c.ID == self.ID {
			foundSelf = true
			continue
		}
		sameProject := c.Labels["com.docker.compose.project"] == project
		if sameProject && c.Labels["com.docker.compose.service"] == "preparation" {
			foundPrep = true
		}
		sharesState := false
		for _, mount := range c.Mounts {
			if mount.Type == "volume" && (mount.Name == project+"_e2-host-state" ||
				mount.Name == project+"_e2-host-ipc" || mount.Name == project+"_product-state") {
				sharesState = true
			}
		}
		if (sameProject && c.Labels["com.docker.compose.service"] != "postgres" || sharesState) &&
			c.State != "exited" && c.State != "created" {
			return "", ErrUnavailable
		}
	}
	if !foundSelf || !foundPrep {
		return "", ErrUnavailable
	}
	body, err := json.Marshal(struct {
		Project    string
		Self       string
		Containers []dockerContainer
	}{project, self.ID, containers})
	if err != nil {
		return "", ErrUnavailable
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}
