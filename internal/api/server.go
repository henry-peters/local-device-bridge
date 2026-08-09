package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/config"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
	"github.com/local-device-bridge/local-device-bridge/internal/security"
)

//go:embed static/*
var staticFiles embed.FS

type Server struct {
	manager    *core.Manager
	cfg        config.Config
	secrets    *security.SecretStore
	logger     *slog.Logger
	token      string
	agentToken string
	settings   *sharedSettings
	configPath string
	pairMu     *sync.Mutex
	pairTokens map[string]time.Time
}

type sharedSettings struct {
	mu    sync.RWMutex
	value core.InventoryVisibility
}

// Version is returned by /api/v1/health so an operator can verify which
// embedded dashboard and adapter build is actually running.
const Version = "0.2.0"

func NewServer(manager *core.Manager, cfg config.Config, secrets *security.SecretStore, logger *slog.Logger) (*Server, error) {
	token, err := EnsureAccessToken(secrets)
	if err != nil {
		return nil, err
	}
	agentToken, err := EnsureAgentToken(secrets)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	settings := core.InventoryVisibility{
		ShowDisplayDevices:  cfg.Discovery.ShowDisplayDevices,
		ShowConsoleDevices:  false,
		ShowComputerDevices: cfg.Discovery.ShowComputerDevices,
		ShowOfflineDevices:  cfg.Discovery.ShowOfflineDevices,
	}
	manager.SetInventoryVisibility(settings)
	return &Server{manager: manager, cfg: cfg, secrets: secrets, logger: logger, token: token, agentToken: agentToken, settings: &sharedSettings{value: settings}, pairMu: &sync.Mutex{}, pairTokens: make(map[string]time.Time)}, nil
}

func EnsureAccessToken(secrets *security.SecretStore) (string, error) {
	return ensureToken(secrets, "dashboard-token")
}

// EnsureAgentToken returns the separate bearer credential for AI agents and
// API clients. It is intentionally not the browser/dashboard unlock token.
func EnsureAgentToken(secrets *security.SecretStore) (string, error) {
	return ensureToken(secrets, "agent-token")
}

// RotateAccessToken replaces the reusable browser fallback token. Phone QR
// links do not use this token; they use a short-lived one-time pairing token.
func RotateAccessToken(secrets *security.SecretStore) (string, error) {
	return rotateToken(secrets, "dashboard-token")
}

// RotateAgentToken revokes the current bearer credential and creates a new one
// for an AI agent or other authorized API client.
func RotateAgentToken(secrets *security.SecretStore) (string, error) {
	return rotateToken(secrets, "agent-token")
}

func ensureToken(secrets *security.SecretStore, key string) (string, error) {
	if token, err := secrets.Get(key); err == nil && token != "" {
		return token, nil
	}
	return rotateToken(secrets, key)
}

func rotateToken(secrets *security.SecretStore, key string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if err := secrets.Set(key, token); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Server) AccessToken() string { return s.token }

func (s *Server) SetConfigPath(path string) { s.configPath = path }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.dashboard)
	mux.HandleFunc("POST /api/v1/auth/session", s.createSession)
	mux.HandleFunc("POST /api/v1/auth/pair", s.pairBrowser)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.Handle("/api/v1/", s.auth(http.HandlerFunc(s.api)))
	return logging(limitRequestBody(mux), s.logger)
}

const maxRequestBodyBytes int64 = 1 << 20

// limitRequestBody keeps malformed or oversized JSON from consuming an
// unbounded amount of memory on the local/LAN control API. The dashboard only
// sends small command, pairing, naming, and settings payloads.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	server := &http.Server{Addr: s.cfg.Server.Bind, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		var err error
		if s.cfg.Server.AllowLAN && !s.cfg.Server.InsecureLANHTTP {
			cert, key, certErr := ensureCertificate(s.cfg)
			if certErr != nil {
				errCh <- certErr
				return
			}
			err = server.ListenAndServeTLS(cert, key)
		} else {
			err = server.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// LocalDashboardBind returns the loopback-only HTTP companion address used on
// the bridge computer when LAN access is enabled. Keeping this endpoint local
// avoids making the host browser trust a private certificate.
func LocalDashboardBind(bind string) string {
	_, port, err := net.SplitHostPort(bind)
	if err != nil || port == "" {
		port = "8787"
	}
	parsed, err := strconv.Atoi(port)
	if err == nil && parsed > 0 && parsed < 65535 {
		port = strconv.Itoa(parsed + 1)
	} else {
		port = "8788"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// ListenAndServeLocal serves the same embedded dashboard over an HTTP
// loopback-only companion listener. The copied configuration deliberately
// disables LAN authentication rules for this local-only endpoint.
func (s *Server) ListenAndServeLocal(ctx context.Context, bind string) error {
	local := &Server{
		manager:    s.manager,
		cfg:        s.cfg,
		secrets:    s.secrets,
		logger:     s.logger,
		token:      s.token,
		agentToken: s.agentToken,
		settings:   s.settings,
		configPath: s.configPath,
		pairMu:     s.pairMu,
		pairTokens: s.pairTokens,
	}
	local.cfg.Server.Bind = bind
	local.cfg.Server.AllowLAN = false
	return local.ListenAndServe(ctx)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	b, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	if strings.HasSuffix(name, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	if strings.HasSuffix(name, ".json") {
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	}
	_, _ = w.Write(b)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Token == "" || !secureEqual(body.Token, s.token) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	s.setSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) issueBrowserPairingToken() (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(b)
	expires := time.Now().Add(10 * time.Minute)
	s.pairMu.Lock()
	for existing, expiry := range s.pairTokens {
		if expiry.Before(time.Now()) {
			delete(s.pairTokens, existing)
		}
	}
	s.pairTokens[token] = expires
	s.pairMu.Unlock()
	return token, expires, nil
}

func (s *Server) consumeBrowserPairingToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	now := time.Now()
	s.pairMu.Lock()
	expires, ok := s.pairTokens[token]
	if ok {
		delete(s.pairTokens, token)
	}
	s.pairMu.Unlock()
	return ok && expires.After(now)
}

func (s *Server) setSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "ldb_session", Value: s.token, Path: "/", HttpOnly: true, Secure: s.cfg.Server.AllowLAN && !s.cfg.Server.InsecureLANHTTP, SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
}

func (s *Server) pairBrowser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || !s.consumeBrowserPairingToken(body.Token) {
		http.Error(w, "pairing link is invalid, expired, or already used; run local-device-bridge dashboard phone again", http.StatusUnauthorized)
		return
	}
	s.setSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Server.AllowLAN && isLoopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" {
			if cookie, err := r.Cookie("ldb_session"); err == nil {
				provided = cookie.Value
			}
		}
		if !secureEqual(provided, s.token) && !secureEqual(provided, s.agentToken) {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case r.Method == http.MethodGet && path == "settings":
		writeJSON(w, http.StatusOK, map[string]any{"settings": s.currentSettings()})
	case r.Method == http.MethodPost && path == "auth/pairing-link":
		token, expires, err := s.issueBrowserPairingToken()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires.UTC(), "expires_in_seconds": int(time.Until(expires).Seconds())})
	case r.Method == http.MethodPost && path == "settings":
		settings, err := s.decodeSettings(r.Body)
		if err != nil {
			http.Error(w, "invalid settings", http.StatusBadRequest)
			return
		}
		if err := s.updateSettings(settings); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": s.currentSettings()})
	case r.Method == http.MethodGet && path == "agent/manifest":
		writeJSON(w, http.StatusOK, s.agentManifest())
	case r.Method == http.MethodGet && path == "agent/openapi.json":
		writeJSON(w, http.StatusOK, s.agentOpenAPI())
	case r.Method == http.MethodGet && path == "devices":
		writeJSON(w, http.StatusOK, map[string]any{"devices": s.manager.List()})
	case r.Method == http.MethodPost && path == "discovery/scan":
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		found, err := s.manager.Scan(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": found})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "devices/") && strings.HasSuffix(path, "/state"):
		id, err := s.resolveDeviceReference(strings.TrimSuffix(strings.TrimPrefix(path, "devices/"), "/state"))
		if err != nil {
			writeError(w, err)
			return
		}
		state, err := s.manager.State(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, state)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "devices/") && strings.HasSuffix(path, "/pair"):
		id, err := s.resolveDeviceReference(strings.TrimSuffix(strings.TrimPrefix(path, "devices/"), "/pair"))
		if err != nil {
			writeError(w, err)
			return
		}
		var body core.PairOptions
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		// Pairing may wake a sleeping TV and wait for its on-screen approval.
		// Keep this request alive long enough for both steps to complete.
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		if err := s.manager.Pair(ctx, id, body); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"paired": true})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "devices/") && strings.HasSuffix(path, "/unpair"):
		id, err := s.resolveDeviceReference(strings.TrimSuffix(strings.TrimPrefix(path, "devices/"), "/unpair"))
		if err != nil {
			writeError(w, err)
			return
		}
		if err := s.manager.Unpair(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"paired": false})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "devices/") && strings.HasSuffix(path, "/name"):
		id, err := s.resolveDeviceReference(strings.TrimSuffix(strings.TrimPrefix(path, "devices/"), "/name"))
		if err != nil {
			writeError(w, err)
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if err := s.manager.Rename(r.Context(), string(id), body.Name); err != nil {
			writeError(w, err)
			return
		}
		for _, item := range s.manager.List() {
			if item.ID == id {
				writeJSON(w, http.StatusOK, map[string]any{"device": item})
				return
			}
		}
		writeError(w, core.ErrDeviceNotFound)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "devices/") && strings.HasSuffix(path, "/guide"):
		id, err := s.resolveDeviceReference(strings.TrimSuffix(strings.TrimPrefix(path, "devices/"), "/guide"))
		if err != nil {
			writeError(w, err)
			return
		}
		guide, err := s.deviceGuide(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, guide)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "devices/") && strings.HasSuffix(path, "/commands"):
		id, err := s.resolveDeviceReference(strings.TrimSuffix(strings.TrimPrefix(path, "devices/"), "/commands"))
		if err != nil {
			writeError(w, err)
			return
		}
		var body struct {
			Action    core.Action       `json:"action"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
			http.Error(w, "action is required", http.StatusBadRequest)
			return
		}
		if !allowedAction(body.Action) {
			http.Error(w, "unsupported action", http.StatusBadRequest)
			return
		}
		result, err := s.manager.Execute(r.Context(), core.Command{DeviceID: id, Action: body.Action, Arguments: body.Arguments, Principal: "dashboard", Source: "http"})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case r.Method == http.MethodGet && path == "events":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := s.manager.Audit(r.Context(), limit)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) resolveDeviceReference(reference string) (core.DeviceID, error) {
	return s.manager.ResolveDeviceReference(reference)
}

func (s *Server) agentManifest() map[string]any {
	return map[string]any{
		"name":              "local-device-bridge",
		"version":           Version,
		"agent_api_version": "1",
		"purpose":           "Local-network device discovery and safe remote control for an AI agent or other authorized client.",
		"base_path":         "/api/v1",
		"authentication": map[string]any{
			"type":   "bearer",
			"header": "Authorization: Bearer <agent-token>",
			"note":   "Use the separate agent token printed by local-device-bridge agent token. The browser dashboard token is different and must not be shared with an agent.",
		},
		"workflow": []string{"GET /devices and choose id, alias, or name", "GET /devices/{device_id}/guide", "Follow the returned steps: wake the device, enable its local access setting, POST /devices/{device_id}/pair, accept the on-device prompt, wait for paired=true", "POST /devices/{device_id}/commands"},
		"endpoints": []map[string]any{
			{"name": "list_devices", "method": http.MethodGet, "path": "/devices", "description": "List visible devices and their capabilities."},
			{"name": "scan_network", "method": http.MethodPost, "path": "/discovery/scan", "description": "Refresh discovery before pairing or when an address may have changed."},
			{"name": "device_guide", "method": http.MethodGet, "path": "/devices/{device_id}/guide", "description": "Return device-specific access and pairing instructions."},
			{"name": "pair_device", "method": http.MethodPost, "path": "/devices/{device_id}/pair", "description": "Start the device pairing flow when the device guide requires it."},
			{"name": "unpair_device", "method": http.MethodPost, "path": "/devices/{device_id}/unpair", "description": "Remove saved pairing credentials."},
			{"name": "rename_device", "method": http.MethodPost, "path": "/devices/{device_id}/name", "description": "Save a unique friendly alias so future agent commands can refer to the device by name."},
			{"name": "device_state", "method": http.MethodGet, "path": "/devices/{device_id}/state", "description": "Read current reachability and reported state."},
			{"name": "execute_command", "method": http.MethodPost, "path": "/devices/{device_id}/commands", "description": "Execute one capability-checked normalized command."},
			{"name": "events", "method": http.MethodGet, "path": "/events", "description": "Read the audit stream for command results."},
		},
		"actions": []map[string]any{
			{"action": core.ActionStatus, "arguments": map[string]string{}, "description": "Read device state."},
			{"action": core.ActionPowerOn, "arguments": map[string]string{}, "description": "Wake or power on when the adapter supports it."},
			{"action": core.ActionPowerOff, "arguments": map[string]string{}, "description": "Power off when the adapter supports it."},
			{"action": core.ActionVolumeUp, "arguments": map[string]string{"steps": "1-20"}, "description": "Increase volume by one or more steps."},
			{"action": core.ActionVolumeDown, "arguments": map[string]string{"steps": "1-20"}, "description": "Decrease volume by one or more steps."},
			{"action": core.ActionVolumeSet, "arguments": map[string]string{"volume": "0-100"}, "description": "Request an absolute level when the adapter supports it."},
			{"action": core.ActionMute, "arguments": map[string]string{}, "description": "Toggle mute."},
			{"action": core.ActionKey, "arguments": map[string]string{"key": "UP|DOWN|LEFT|RIGHT|ENTER|RETURN|HOME|PLAYPAUSE"}, "description": "Send a common remote key, including the media play/pause toggle."},
			{"action": core.ActionSource, "arguments": map[string]string{"source": "adapter-specific"}, "description": "Select an input/source when supported."},
			{"action": core.ActionChannel, "arguments": map[string]string{"channel": "adapter-specific"}, "description": "Change or enter a channel when supported."},
		},
		"devices": s.manager.List(),
	}
}

// agentOpenAPI is generated by the running bridge so its contract is always
// paired with the binary that implements it. The manifest remains the source
// of host-specific device capabilities and setup instructions.
func (s *Server) agentOpenAPI() map[string]any {
	parameter := []map[string]any{{"name": "device_id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}
	commandSchema := map[string]any{
		"type":     "object",
		"required": []string{"action"},
		"properties": map[string]any{
			"action":    map[string]any{"type": "string", "enum": []string{string(core.ActionStatus), string(core.ActionPowerOn), string(core.ActionPowerOff), string(core.ActionVolumeUp), string(core.ActionVolumeDown), string(core.ActionVolumeSet), string(core.ActionMute), string(core.ActionKey), string(core.ActionSource), string(core.ActionChannel)}},
			"arguments": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Use the argument names and values returned by the agent manifest and device guide."},
		},
	}
	paths := map[string]any{
		"/health":                       map[string]any{"get": map[string]any{"security": []any{}, "responses": map[string]any{"200": map[string]any{"description": "Bridge is healthy"}}}},
		"/devices":                      map[string]any{"get": map[string]any{"summary": "List discovered devices", "responses": map[string]any{"200": map[string]any{"description": "Device inventory"}}}},
		"/discovery/scan":               map[string]any{"post": map[string]any{"summary": "Refresh local discovery", "responses": map[string]any{"200": map[string]any{"description": "Updated device inventory"}}}},
		"/devices/{device_id}/guide":    map[string]any{"get": map[string]any{"summary": "Read exact setup and pairing steps", "parameters": parameter, "responses": map[string]any{"200": map[string]any{"description": "Device-specific guide"}}}},
		"/devices/{device_id}/pair":     map[string]any{"post": map[string]any{"summary": "Pair when the guide requires it", "parameters": parameter, "responses": map[string]any{"200": map[string]any{"description": "Pairing completed"}}}},
		"/devices/{device_id}/unpair":   map[string]any{"post": map[string]any{"summary": "Remove saved pairing credentials", "parameters": parameter, "responses": map[string]any{"200": map[string]any{"description": "Pairing removed"}}}},
		"/devices/{device_id}/name":     map[string]any{"post": map[string]any{"summary": "Set a friendly device alias", "parameters": parameter, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string", "maxLength": 64}}}}}}, "responses": map[string]any{"200": map[string]any{"description": "Renamed device"}}}},
		"/devices/{device_id}/state":    map[string]any{"get": map[string]any{"summary": "Read current device state", "parameters": parameter, "responses": map[string]any{"200": map[string]any{"description": "Device state"}}}},
		"/devices/{device_id}/commands": map[string]any{"post": map[string]any{"summary": "Execute one capability-checked normalized command", "parameters": parameter, "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": commandSchema}}}, "responses": map[string]any{"200": map[string]any{"description": "Command result"}}}},
		"/events":                       map[string]any{"get": map[string]any{"summary": "Read command audit events", "responses": map[string]any{"200": map[string]any{"description": "Audit events"}}}},
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "local-device-bridge agent API",
			"version":     Version,
			"description": "Local-only, bearer-authenticated discovery and capability-checked control. Read the generated agent manifest and device guide before executing commands.",
		},
		"servers":  []map[string]string{{"url": "/api/v1"}},
		"security": []map[string][]string{{"bearerAuth": {}}},
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"}},
			"schemas":         map[string]any{"CommandRequest": commandSchema},
		},
		"paths": paths,
	}
}

func (s *Server) deviceGuide(id core.DeviceID) (map[string]any, error) {
	var device core.DeviceMetadata
	found := false
	for _, item := range s.manager.List() {
		if item.ID == id {
			device = item
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("device %q not found; scan the network and list devices again", id)
	}
	guide := map[string]any{
		"device": device,
		"ready":  device.Paired || device.Manufacturer == "Roku" || device.Discovery == "host",
		"steps":  []string{"Keep the bridge and device on the same trusted LAN.", "Use only the actions listed in the device capabilities.", "If a command fails, read the returned error and the audit event before retrying."},
	}
	switch {
	case strings.EqualFold(device.Manufacturer, "Samsung"):
		guide["pairing"] = device.ControlVerified && !device.Paired
		if !device.ControlVerified && !device.Paired {
			guide["ready"] = false
			guide["steps"] = []string{"Wake the TV and keep it on the same trusted LAN as the bridge.", "Open Settings → General → Network → Expert Settings and enable Power On with Mobile.", "Open Settings → General → External Device Manager → Device Connect Manager and enable Access Notification.", "Run a network scan. Pairing is offered only after the bridge verifies Samsung’s local /api/v2/ service.", "If the TV does not become verified, the bridge will not send a pairing request; check guest Wi‑Fi, client isolation, or the TV’s current address."}
		} else {
			guide["steps"] = []string{"Wake the TV.", "For wake, open Settings → General → Network → Expert Settings and enable Power On with Mobile.", "For remote access, open Settings → General → External Device Manager → Device Connect Manager; enable Access Notification and remove blocked bridge entries from Device List. Newer models may place these under Connection → Network.", "Choose Pair TV in the dashboard or run pair <device-id> exactly once.", "Accept the on-screen prompt and wait for paired=true before sending commands."}
		}
	case strings.EqualFold(device.Manufacturer, "Roku"):
		guide["pairing"] = false
		guide["steps"] = []string{"On the Roku TV, open Settings → System → Advanced system settings → Control by mobile apps.", "Set Control by mobile apps to Enabled.", "Keep the TV and bridge on the same trusted LAN.", "Use the normalized remote commands; Roku does not show a bridge pairing prompt."}
	case strings.EqualFold(device.Manufacturer, "Apple") && device.Kind == core.DeviceKindComputer:
		guide["pairing"] = device.Discovery != "host"
		guide["steps"] = []string{"Enable Remote Login on the target Mac.", "Use the dashboard-generated restricted setup commands.", "Pair the target Mac with its short login name.", "Only status, Wake, and Sleep are exposed; the bridge host remains status-only."}
	case device.Kind == core.DeviceKindComputer && strings.EqualFold(device.Platform, "Raspberry Pi"):
		guide["pairing"] = false
		guide["steps"] = []string{"The Raspberry Pi has been identified on the LAN.", "No remote-login credential is requested because Linux control is not implemented in this release.", "Do not enter an SSH password or key into the dashboard; the device remains inventory-only until a restricted Linux adapter is added and tested."}
	case device.Kind == core.DeviceKindComputer && (strings.EqualFold(device.Platform, "Windows laptop") || strings.EqualFold(device.Platform, "Windows")):
		guide["pairing"] = false
		guide["steps"] = []string{"This Windows computer is identified on the LAN.", "Windows Remote Management and Remote Desktop are not universal pairing protocols and are intentionally not enabled automatically.", "This release keeps the computer inventory-only until a restricted, authenticated Windows helper is implemented and tested."}
	case strings.EqualFold(device.Manufacturer, "LG") && device.Kind == core.DeviceKindTV:
		guide["pairing"] = false
		guide["steps"] = []string{"Enable LG Connect Apps / Mobile TV On in the TV network settings.", "LG webOS requires its own per-TV pairing key.", "This build identifies the TV but does not enable controls until a tested webOS adapter is available."}
	case strings.EqualFold(device.Manufacturer, "Sony") && device.Kind == core.DeviceKindTV:
		guide["pairing"] = false
		guide["steps"] = []string{"Open Settings → Network & Internet → Home network → IP control; labels vary by BRAVIA model.", "Sony IP control is model-dependent and authenticated.", "This build keeps controls disabled unless a supported adapter is detected."}
	case device.Kind == core.DeviceKindConsole:
		guide["pairing"] = false
		guide["steps"] = consoleGuide(device)
	default:
		guide["pairing"] = false
		guide["steps"] = []string{"This device is discovered, but no safe control pairing workflow is available yet."}
	}
	return guide, nil
}

func consoleGuide(device core.DeviceMetadata) []string {
	switch strings.ToLower(device.Platform) {
	case "playstation":
		return []string{"On PS5, open Settings → System → Remote Play and enable Remote Play.", "For network wake from Rest Mode, open Settings → System → Power Saving → Features Available in Rest Mode; enable Stay Connected to the Internet and Enable Turning On PS5 from Network.", "Use Sony PS Remote Play for authenticated control; the bridge does not impersonate a PlayStation account."}
	case "xbox":
		return []string{"Enable Remote features and a network-connected sleep mode on the Xbox.", "Use the official Xbox app for authenticated remote control; no stable public universal LAN power API is exposed here."}
	case "nintendo":
		return []string{"Nintendo does not publish a supported LAN remote/power API for this bridge.", "The console is retained as a discovery-only inventory record."}
	default:
		return []string{"This console is discovered, but no safe supported local control adapter is available."}
	}
}

func (s *Server) decodeSettings(reader io.Reader) (core.InventoryVisibility, error) {
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(reader).Decode(&patch); err != nil {
		return core.InventoryVisibility{}, err
	}
	settings := s.currentSettings()
	values := map[string]*bool{
		"show_display_devices":  &settings.ShowDisplayDevices,
		"show_computer_devices": &settings.ShowComputerDevices,
		"show_offline_devices":  &settings.ShowOfflineDevices,
	}
	for key, raw := range patch {
		target, ok := values[key]
		if !ok {
			continue
		}
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return core.InventoryVisibility{}, err
		}
		*target = value
	}
	return settings, nil
}

func (s *Server) currentSettings() core.InventoryVisibility {
	s.settings.mu.RLock()
	defer s.settings.mu.RUnlock()
	return s.settings.value
}

func (s *Server) updateSettings(settings core.InventoryVisibility) error {
	settings.ShowConsoleDevices = false
	if s.configPath != "" {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return fmt.Errorf("load configuration: %w", err)
		}
		cfg.Discovery.ShowDisplayDevices = settings.ShowDisplayDevices
		cfg.Discovery.ShowConsoleDevices = false
		cfg.Discovery.ShowComputerDevices = settings.ShowComputerDevices
		cfg.Discovery.ShowOfflineDevices = settings.ShowOfflineDevices
		if err := config.Save(s.configPath, cfg); err != nil {
			return fmt.Errorf("save dashboard settings: %w", err)
		}
	}
	s.settings.mu.Lock()
	s.settings.value = settings
	s.settings.mu.Unlock()
	s.manager.SetInventoryVisibility(settings)
	return nil
}

func allowedAction(action core.Action) bool {
	switch action {
	case core.ActionStatus, core.ActionPowerOn, core.ActionPowerOff, core.ActionVolumeUp, core.ActionVolumeDown, core.ActionVolumeSet, core.ActionMute, core.ActionKey, core.ActionSource, core.ActionChannel:
		return true
	}
	return false
}

func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func secureEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func logging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).String())
	})
}

func ensureCertificate(cfg config.Config) (string, string, error) {
	cert, key := cfg.Server.TLSCert, cfg.Server.TLSKey
	if cert != "" && key != "" {
		return cert, key, nil
	}
	cert = filepath.Join(cfg.State.Directory, "server.crt")
	key = filepath.Join(cfg.State.Directory, "server.key")
	if _, certErr := os.Stat(cert); certErr == nil {
		if _, keyErr := os.Stat(key); keyErr == nil && certificateMatchesLocalIPs(cert) {
			return cert, key, nil
		}
	}
	if err := generateCertificate(cert, key); err != nil {
		return "", "", err
	}
	return cert, key, nil
}

var _ embed.FS
