package api

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/local-device-bridge/local-device-bridge/internal/config"
	"github.com/local-device-bridge/local-device-bridge/internal/core"
	"github.com/local-device-bridge/local-device-bridge/internal/security"
)

type apiStore struct {
	devices []core.DeviceMetadata
}

func (s apiStore) LoadDevices(context.Context) ([]core.DeviceMetadata, error) { return s.devices, nil }
func (apiStore) SaveDevice(context.Context, core.DeviceMetadata) error        { return nil }
func (apiStore) Audit(context.Context, core.Command, bool, string) error      { return nil }
func (apiStore) Close() error                                                 { return nil }

func TestServerAuthenticationAndDashboard(t *testing.T) {
	manager, err := core.NewManager(apiStore{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.State.Directory = t.TempDir()
	secrets := security.NewSecretStore("api-test", cfg.State.Directory)
	server, err := NewServer(manager, cfg, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := EnsureAgentToken(secrets)
	if err != nil {
		t.Fatal(err)
	}
	if agentToken == server.AccessToken() {
		t.Fatal("agent and dashboard tokens must be distinct")
	}
	handler := server.Handler()
	root := httptest.NewRecorder()
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("root status = %d", root.Code)
	}
	if !strings.Contains(root.Body.String(), "/dashboard.css") || !strings.Contains(root.Body.String(), "/manifest.json") {
		t.Fatal("dashboard does not load the dashboard stylesheet")
	}
	manifest := httptest.NewRecorder()
	handler.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/manifest.json", nil))
	if manifest.Code != http.StatusOK || !strings.Contains(manifest.Body.String(), "Local Device Bridge") {
		t.Fatalf("manifest was not served: status=%d", manifest.Code)
	}
	dashboardCSS := httptest.NewRecorder()
	handler.ServeHTTP(dashboardCSS, httptest.NewRequest(http.MethodGet, "/dashboard.css", nil))
	if dashboardCSS.Code != http.StatusOK || !strings.Contains(dashboardCSS.Body.String(), ".remote-nav") {
		t.Fatalf("dashboard stylesheet was not served: status=%d", dashboardCSS.Code)
	}
	appJS := httptest.NewRecorder()
	handler.ServeHTTP(appJS, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if appJS.Code != http.StatusOK || !strings.Contains(appJS.Body.String(), "Open device") || !strings.Contains(appJS.Body.String(), "loadSettings") || !strings.Contains(appJS.Body.String(), "loadAgentAPI") || !strings.Contains(appJS.Body.String(), "data-command-filter") || strings.Contains(appJS.Body.String(), "Netflix") || strings.Contains(appJS.Body.String(), "YouTube") {
		t.Fatalf("unexpected dashboard script asset: status=%d", appJS.Code)
	}
	if !strings.Contains(root.Body.String(), "Inventory settings") || !strings.Contains(root.Body.String(), "Computers") || !strings.Contains(root.Body.String(), "Search log") || !strings.Contains(root.Body.String(), "Agent API") {
		t.Fatal("dashboard does not expose its settings view")
	}
	if !strings.Contains(dashboardCSS.Body.String(), "#0f172a") || !strings.Contains(dashboardCSS.Body.String(), "#06b6d4") || !strings.Contains(dashboardCSS.Body.String(), "#6366f1") || !strings.Contains(dashboardCSS.Body.String(), "#ef4444") {
		t.Fatal("dashboard stylesheet is missing the Cyber-Slate design tokens")
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	localRequest := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	localRequest.RemoteAddr = "127.0.0.1:45678"
	localResponse := httptest.NewRecorder()
	handler.ServeHTTP(localResponse, localRequest)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("loopback dashboard should not require login, status = %d", localResponse.Code)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d", authorized.Code)
	}
	agentRequest := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	agentRequest.Header.Set("Authorization", "Bearer "+agentToken)
	agentResponse := httptest.NewRecorder()
	handler.ServeHTTP(agentResponse, agentRequest)
	if agentResponse.Code != http.StatusOK {
		t.Fatalf("agent token authorization status = %d", agentResponse.Code)
	}
	linkRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pairing-link", nil)
	linkRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	linkResponse := httptest.NewRecorder()
	handler.ServeHTTP(linkResponse, linkRequest)
	if linkResponse.Code != http.StatusOK || !strings.Contains(linkResponse.Body.String(), `"token"`) {
		t.Fatalf("pairing-link response = %d %s", linkResponse.Code, linkResponse.Body.String())
	}
	var link struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(linkResponse.Body.Bytes(), &link); err != nil || link.Token == "" {
		t.Fatalf("pairing-link token = %q, error = %v", link.Token, err)
	}
	pairRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pair", strings.NewReader(`{"token":"`+link.Token+`"}`))
	pairRequest.RemoteAddr = "192.0.2.45:1234"
	pairResponse := httptest.NewRecorder()
	handler.ServeHTTP(pairResponse, pairRequest)
	if pairResponse.Code != http.StatusOK {
		t.Fatalf("pairing-link exchange = %d %s", pairResponse.Code, pairResponse.Body.String())
	}
	cookies := pairResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("pairing-link exchange returned %d cookies", len(cookies))
	}
	pairedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	pairedRequest.AddCookie(cookies[0])
	pairedResponse := httptest.NewRecorder()
	handler.ServeHTTP(pairedResponse, pairedRequest)
	if pairedResponse.Code != http.StatusOK {
		t.Fatalf("paired browser request = %d", pairedResponse.Code)
	}
	reusedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pair", strings.NewReader(`{"token":"`+link.Token+`"}`))
	reusedResponse := httptest.NewRecorder()
	handler.ServeHTTP(reusedResponse, reusedRequest)
	if reusedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("pairing link was reusable: status = %d", reusedResponse.Code)
	}
	settingsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	settingsRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	settings := httptest.NewRecorder()
	handler.ServeHTTP(settings, settingsRequest)
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), "show_computer_devices") || !strings.Contains(settings.Body.String(), "show_display_devices") {
		t.Fatalf("settings response = %d %s", settings.Code, settings.Body.String())
	}
	updateRequest := httptest.NewRequest(http.MethodPost, "/api/v1/settings", strings.NewReader(`{"show_computer_devices":false,"show_offline_devices":false}`))
	updateRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, updateRequest)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"show_computer_devices":false`) {
		t.Fatalf("settings update = %d %s", updated.Code, updated.Body.String())
	}
	manifestRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/manifest", nil)
	manifestRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, manifestRequest)
	if manifestResponse.Code != http.StatusOK || !strings.Contains(manifestResponse.Body.String(), `"agent_api_version":"1"`) || !strings.Contains(manifestResponse.Body.String(), `"execute_command"`) {
		t.Fatalf("agent manifest = %d %s", manifestResponse.Code, manifestResponse.Body.String())
	}
	openAPIRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent/openapi.json", nil)
	openAPIRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	openAPIResponse := httptest.NewRecorder()
	handler.ServeHTTP(openAPIResponse, openAPIRequest)
	if openAPIResponse.Code != http.StatusOK || !strings.Contains(openAPIResponse.Body.String(), `"openapi":"3.0.3"`) || !strings.Contains(openAPIResponse.Body.String(), "/devices/{device_id}/commands") {
		t.Fatalf("agent OpenAPI = %d %s", openAPIResponse.Code, openAPIResponse.Body.String())
	}
}

func TestGeneratedCertificateCoversCurrentLocalIPs(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Default()
	cfg.State.Directory = directory
	cfg.Server.TLSCert = ""
	cfg.Server.TLSKey = ""
	certPath, keyPath, err := ensureCertificate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("generated key is missing: %v", err)
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("generated certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !certificateMatchesLocalIPs(certPath) {
		t.Fatalf("generated certificate does not cover current local IPs: %v", certificate.IPAddresses)
	}
}

func TestFriendlyNameAPIResolvesAndPersistsAlias(t *testing.T) {
	store := apiStore{devices: []core.DeviceMetadata{{
		ID: "tv-1", Kind: core.DeviceKindTV, Manufacturer: "Samsung", Name: "Living Room TV", IP: "192.0.2.20",
	}}}
	manager, err := core.NewManager(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.State.Directory = t.TempDir()
	secrets := security.NewSecretStore("api-alias-test", cfg.State.Directory)
	server, err := NewServer(manager, cfg, secrets, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/devices/tv-1/name", bytes.NewBufferString(`{"name":"Living Room TV"}`))
	request.Header.Set("Authorization", "Bearer "+server.AccessToken())
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"alias":"Living Room TV"`) {
		t.Fatalf("rename response = %d %s", response.Code, response.Body.String())
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	listRequest.Header.Set("Authorization", "Bearer "+server.AccessToken())
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"alias":"Living Room TV"`) {
		t.Fatalf("aliased inventory = %d %s", listResponse.Code, listResponse.Body.String())
	}
}
