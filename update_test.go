package coredns_omada

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
)

func setupTestServer() *httptest.Server {

	controllerId := "123bee230c77bbb45d9c8545d04d700a"
	siteId := "Default"
	pathLogin := fmt.Sprintf("/%s/api/v2/login", controllerId)
	pathUsers := fmt.Sprintf("/%s/api/v2/users/current", controllerId)
	pathClients := fmt.Sprintf("/%s/api/v2/sites/%s/clients", controllerId, siteId)
	pathDevices := fmt.Sprintf("/%s/api/v2/sites/%s/devices", controllerId, siteId)
	pathNetworks := fmt.Sprintf("/%s/api/v2/sites/%s/setting/lan/networks", controllerId, siteId)
	pathDhcp := fmt.Sprintf("/%s/api/v2/sites/%s/setting/service/dhcp", controllerId, siteId)

	responses := map[string]string{
		"/api/info":  "./test-data/info-response.json",
		pathLogin:    "./test-data/login-response.json",
		pathUsers:    "./test-data/users-response.json",
		pathClients:  "./test-data/clients-response.json",
		pathDevices:  "./test-data/devices-response.json",
		pathNetworks: "./test-data/networks-response.json",
		pathDhcp:     "./test-data/dhcp-reservation-response.json",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseFile, ok := responses[r.URL.Path]
		if !ok {
			log.Fatalf("Unexpected request path on mock server: %s", r.URL.Path)
		}
		response, err := os.ReadFile(responseFile)
		if err != nil {
			log.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(response)
	}))

	return server
}

func TestUpdate(t *testing.T) {

	testServer := setupTestServer()
	defer testServer.Close()

	url := testServer.URL
	u := "test"
	p := "test"
	testOmada, err := NewOmada(context.TODO(), url, u, p)
	if err != nil {
		t.Fatalf("test failure on 'TestUpdate/NewOmada': %v", err)
	}
	testOmada.Next = testHandler()

	err = testOmada.controllerInit(context.TODO())
	if err != nil {
		t.Fatalf("test failure on 'TestUpdate/controllerInit': %v", err)
	}

	testOmada.config.refresh_minutes = 1
	testOmada.config.refresh_login_hours = 24
	testOmada.config.resolve_clients = true
	testOmada.config.resolve_devices = true
	testOmada.config.resolve_dhcp_reservations = true
	testOmada.config.stale_record_duration, _ = time.ParseDuration("5m")
	var sites []string
	for s := range testOmada.controller.Sites {
		sites = append(sites, s)
	}
	testOmada.sites = sites
	assert.Len(t, testOmada.sites, 1)

	err = testOmada.updateZones()
	if err != nil {
		t.Fatalf("test failure on 'TestUpdate/updateZones': %v", err)
	}

	assert.Len(t, testOmada.zoneNames, 3)
	assert.Len(t, testOmada.zones, 3)
	assert.Equal(t, 13, testOmada.zones["omada.home."].Count)

	tests := []testCases{
		{ // foward resolve: client
			qname:      "client-001.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"client-001.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // foward resolve: DHCP reservation
			qname:      "client-01.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"client-01.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // disabled DHCP reservation
			qname:        "disabled-dhcp-01.omada.home.",
			qtype:        dns.TypeA,
			wantRetCode:  dns.RcodeServerFailure,
			wantMsgRCode: dns.RcodeServerFailure,
		},
		{ // fail: non existent client
			qname:        "client-002.omada.home.",
			qtype:        dns.TypeA,
			wantRetCode:  dns.RcodeServerFailure,
			wantMsgRCode: dns.RcodeServerFailure,
		},
		{ // ptr resolve: client
			qname:      "102.0.0.10.in-addr.arpa.",
			qtype:      dns.TypePTR,
			wantAnswer: []string{"102.0.0.10.in-addr.arpa.	60	IN	PTR	win10-vm.omada.home."},
		},
		{ // ptr - DHCP reservation takes priority over client
			qname:      "101.0.0.10.in-addr.arpa.",
			qtype:      dns.TypePTR,
			wantAnswer: []string{"101.0.0.10.in-addr.arpa.	60	IN	PTR	client-01.omada.home."},
		},
		// wildcard dhcp reservation
		{
			qname:      "test.kubernetes.omada.home",
			qtype:      dns.TypeA,
			wantAnswer: []string{"test.kubernetes.omada.home.	60	IN	A	10.0.0.150"},
		},
	}
	executeTestCases(t, testOmada, tests)
}

func TestUpdateWithFallback(t *testing.T) {
	testServer := setupTestServer()
	defer testServer.Close()

	url := testServer.URL
	u := "test"
	p := "test"
	testOmada, err := NewOmada(context.TODO(), url, u, p)
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithFallback/NewOmada': %v", err)
	}
	testOmada.Next = testHandler()

	err = testOmada.controllerInit(context.TODO())
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithFallback/controllerInit': %v", err)
	}

	// Configure with fallback IP
	testOmada.config.refresh_minutes = 1
	testOmada.config.refresh_login_hours = 24
	testOmada.config.resolve_clients = true
	testOmada.config.resolve_devices = true
	testOmada.config.resolve_dhcp_reservations = true
	testOmada.config.stale_record_duration, _ = time.ParseDuration("5m")
	testOmada.config.fallback = "10.0.0.200" // Fallback IP

	var sites []string
	for s := range testOmada.controller.Sites {
		sites = append(sites, s)
	}
	testOmada.sites = sites
	assert.Len(t, testOmada.sites, 1)

	err = testOmada.updateZones()
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithFallback/updateZones': %v", err)
	}

	assert.Len(t, testOmada.zoneNames, 3)
	assert.Len(t, testOmada.zones, 3)
	// Should have one more record due to wildcard fallback
	assert.Equal(t, 14, testOmada.zones["omada.home."].Count)

	tests := []testCases{
		{ // existing record should work normally
			qname:      "client-001.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"client-001.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // non-existent record should fallback to wildcard
			qname:      "nonexistent.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"nonexistent.omada.home.	60	IN	A	10.0.0.200"},
		},
		{ // another non-existent record should also fallback
			qname:      "app.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"app.omada.home.	60	IN	A	10.0.0.200"},
		},
		{ // existing wildcard reservation should take precedence
			qname:      "test.kubernetes.omada.home",
			qtype:      dns.TypeA,
			wantAnswer: []string{"test.kubernetes.omada.home.	60	IN	A	10.0.0.150"},
		},
	}
	executeTestCases(t, testOmada, tests)
}

func TestUpdateWithLocalHostnameFallback(t *testing.T) {
	testServer := setupTestServer()
	defer testServer.Close()

	url := testServer.URL
	u := "test"
	p := "test"
	testOmada, err := NewOmada(context.TODO(), url, u, p)
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithLocalHostnameFallback/NewOmada': %v", err)
	}
	testOmada.Next = testHandler()

	err = testOmada.controllerInit(context.TODO())
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithLocalHostnameFallback/controllerInit': %v", err)
	}

	// Configure with local hostname fallback that should resolve to existing record
	testOmada.config.refresh_minutes = 1
	testOmada.config.refresh_login_hours = 24
	testOmada.config.resolve_clients = true
	testOmada.config.resolve_devices = true
	testOmada.config.resolve_dhcp_reservations = true
	testOmada.config.stale_record_duration, _ = time.ParseDuration("5m")
	testOmada.config.fallback = "client-001" // Should resolve to existing client record

	var sites []string
	for s := range testOmada.controller.Sites {
		sites = append(sites, s)
	}
	testOmada.sites = sites

	err = testOmada.updateZones()
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithLocalHostnameFallback/updateZones': %v", err)
	}

	// Should have one more record due to wildcard fallback
	assert.Equal(t, 14, testOmada.zones["omada.home."].Count)

	tests := []testCases{
		{ // existing record should work normally
			qname:      "client-001.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"client-001.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // existing record should work normally
			qname:      "win10-vm.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"win10-vm.omada.home.	60	IN	A	10.0.0.102"},
		},
		{ // non-existent record should fallback to client-001 IP
			qname:      "app.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"app.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // another non-existent record should also fallback
			qname:      "service.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"service.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // existing wildcard reservation should take precedence
			qname:      "test.kubernetes.omada.home",
			qtype:      dns.TypeA,
			wantAnswer: []string{"test.kubernetes.omada.home.	60	IN	A	10.0.0.150"},
		},
	}
	executeTestCases(t, testOmada, tests)
}

func TestUpdateWithFqdnFallback(t *testing.T) {
	testServer := setupTestServer()
	defer testServer.Close()

	url := testServer.URL
	u := "test"
	p := "test"
	testOmada, err := NewOmada(context.TODO(), url, u, p)
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithFqdnFallback/NewOmada': %v", err)
	}
	testOmada.Next = testHandler()

	err = testOmada.controllerInit(context.TODO())
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithFqdnFallback/controllerInit': %v", err)
	}

	// Configure with FQDN fallback that should resolve to existing record
	testOmada.config.refresh_minutes = 1
	testOmada.config.refresh_login_hours = 24
	testOmada.config.resolve_clients = true
	testOmada.config.resolve_devices = true
	testOmada.config.resolve_dhcp_reservations = true
	testOmada.config.stale_record_duration, _ = time.ParseDuration("5m")
	testOmada.config.fallback = "client-001.omada.home." // Should resolve to existing client FQDN

	var sites []string
	for s := range testOmada.controller.Sites {
		sites = append(sites, s)
	}
	testOmada.sites = sites

	err = testOmada.updateZones()
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithFqdnFallback/updateZones': %v", err)
	}

	// Should have one more record due to wildcard fallback
	assert.Equal(t, 14, testOmada.zones["omada.home."].Count)

	tests := []testCases{
		{ // existing record should work normally
			qname:      "client-001.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"client-001.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // non-existent record should fallback to client-001 FQDN IP
			qname:      "app.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"app.omada.home.	60	IN	A	10.0.0.101"},
		},
	}

	// Execute base test cases
	executeTestCases(t, testOmada, tests)
}

func TestUpdateWithHostnameNotFoundFallback(t *testing.T) {
	testServer := setupTestServer()
	defer testServer.Close()

	url := testServer.URL
	u := "test"
	p := "test"
	testOmada, err := NewOmada(context.TODO(), url, u, p)
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithHostnameFallback/NewOmada': %v", err)
	}
	testOmada.Next = testHandler()

	err = testOmada.controllerInit(context.TODO())
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithHostnameFallback/controllerInit': %v", err)
	}

	// Configure with hostname fallback that won't be found (testing warning case)
	testOmada.config.refresh_minutes = 1
	testOmada.config.refresh_login_hours = 24
	testOmada.config.resolve_clients = true
	testOmada.config.resolve_devices = true
	testOmada.config.resolve_dhcp_reservations = true
	testOmada.config.stale_record_duration, _ = time.ParseDuration("5m")
	testOmada.config.fallback = "nonexistent-host" // Should not be found

	var sites []string
	for s := range testOmada.controller.Sites {
		sites = append(sites, s)
	}
	testOmada.sites = sites

	err = testOmada.updateZones()
	if err != nil {
		t.Fatalf("test failure on 'TestUpdateWithHostnameFallback/updateZones': %v", err)
	}

	// Should NOT have extra record since fallback wasn't found
	assert.Equal(t, 13, testOmada.zones["omada.home."].Count)

	tests := []testCases{
		{ // existing record should work normally
			qname:      "client-001.omada.home.",
			qtype:      dns.TypeA,
			wantAnswer: []string{"client-001.omada.home.	60	IN	A	10.0.0.101"},
		},
		{ // non-existent record should fail (no fallback added)
			qname:        "app.omada.home.",
			qtype:        dns.TypeA,
			wantRetCode:  dns.RcodeServerFailure,
			wantMsgRCode: dns.RcodeServerFailure,
		},
	}
	executeTestCases(t, testOmada, tests)

}
