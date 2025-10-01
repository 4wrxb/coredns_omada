package coredns_omada

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/coredns/coredns/plugin/file"
	omada "github.com/dougbw/go-omada"
	"github.com/miekg/dns"
)

type ARecord struct {
	record    *dns.A
	timestamp time.Time
}

type PtrRecord struct {
	record    *dns.PTR
	timestamp time.Time
}

type DnsRecords struct {
	ARecords   map[string]ARecord
	PtrRecords map[string]PtrRecord
}

// refresh the DNS zones every X minutes
func updateZoneLoop(ctx context.Context, o *Omada) {

	delay := time.Duration(o.config.refresh_minutes) * time.Minute
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			log.Debugf("Breaking out of zone update loop: %v", ctx.Err())
			return
		case <-timer.C:
			if err := o.updateZones(); err != nil && ctx.Err() == nil {
				log.Errorf("Failed to update zones: %v", err)
			}
		}
	}
}

// refresh the login session token every X hours
func updateSessionLoop(ctx context.Context, o *Omada) {

	delay := time.Duration(o.config.refresh_login_hours) * time.Hour
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			log.Debugf("Breaking out of login update loop: %v", ctx.Err())
			return
		case <-timer.C:
			if err := o.login(); err != nil && ctx.Err() == nil {
				log.Errorf("Failed to login to controller : %v", err)
			}
		}
	}
}

// update dns zones
func (o *Omada) updateZones() error {

	log.Info("update: updating zones...")

	var networks []omada.OmadaNetwork
	var clients []omada.Client
	var devices []omada.Device
	var reservations []omada.DhcpReservation
	for _, s := range o.sites {

		log.Debugf("update: getting networks for site: %s", s)
		o.controller.SetSite(s)
		n, err := o.controller.GetNetworks()
		if err != nil {
			return fmt.Errorf("error getting networks from omada controller: %w", err)
		}
		interfaces := getInterfaces(n)
		networks = append(networks, interfaces...)

		if o.config.resolve_clients {
			log.Debugf("update: getting clients for site: %s", s)
			c, err := o.controller.GetClients()
			if err != nil {
				return fmt.Errorf("error getting clients from omada controller: %w", err)
			}
			clients = append(clients, c...)
		}

		if o.config.resolve_devices {
			log.Debugf("update: getting devices for site: %s", s)
			d, err := o.controller.GetDevices()
			if err != nil {
				return fmt.Errorf("error getting devices from omada controller: %w", err)
			}
			devices = append(devices, d...)
		}

		if o.config.resolve_dhcp_reservations {
			log.Debugf("update: getting dhcp reservations for site: %s", s)
			r, err := o.controller.GetDhcpReservations()
			if err != nil {
				return fmt.Errorf("error getting dhcp reservations from omada controller: %w", err)
			}
			reservations = append(reservations, r...)
		}
	}
	if o.config.resolve_clients {
		log.Debugf("update: found '%d' clients\n", len(clients))
	}

	if o.config.resolve_devices {
		log.Debugf("update: found '%d' devices\n", len(devices))
	}

	if o.config.resolve_dhcp_reservations {
		log.Debugf("update: found '%d' reservations\n", len(reservations))
	}

	records := o.records
	_, ok := records[ptrZone]
	if !ok {
		records[ptrZone] = DnsRecords{
			ARecords:   make(map[string]ARecord),
			PtrRecords: make(map[string]PtrRecord),
		}
	}

	timestamp := time.Now()
	for _, network := range networks {
		log.Debugf("update: -- processing network: %s", network.Name)

		// skip network if no dns search domain is set
		if network.Domain == "" {
			log.Debugf("update: skipping network: %s because not DNS search domain is set", network.Name)
			continue
		}
		dnsDomain := network.Domain + "."

		// create record map
		_, ok := records[dnsDomain]
		if !ok {
			log.Debugf("update: creating record map: %s", dnsDomain)
			records[dnsDomain] = DnsRecords{
				ARecords:   make(map[string]ARecord),
				PtrRecords: make(map[string]PtrRecord),
			}
		}

		// process client records
		log.Debugf("update: adding records to zone: %s\n", dnsDomain)
		_, subnet, err := net.ParseCIDR(network.Subnet)
		if err != nil {
			log.Debugf("failed to parse network cidr: %v, %v", err, subnet)
			continue
		}
		for _, client := range clients {

			// if client is in this networks subnet then add record to zone
			ip := net.ParseIP(client.Ip)
			if ip == nil {
				continue
			}
			if subnet.Contains(ip) {
				dnsName := client.Name
				if client.Name == client.MAC && client.HostName != "--" {
					dnsName = client.HostName
				}
				clientFqdn := fmt.Sprintf("%s.%s", makeDNSSafe(dnsName), dnsDomain)
				a := &dns.A{Hdr: dns.RR_Header{Name: clientFqdn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A: net.ParseIP(client.Ip)}
				records[dnsDomain].ARecords[clientFqdn] = ARecord{
					record:    a,
					timestamp: timestamp,
				}

				ptrName := getPtrZoneFromIp(client.Ip)
				ptr := &dns.PTR{Hdr: dns.RR_Header{Name: ptrName, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 60},
					Ptr: dns.Fqdn(clientFqdn)}
				records[ptrZone].PtrRecords[ptrName] = PtrRecord{
					record:    ptr,
					timestamp: timestamp,
				}
			}
		}

		// process device records
		for _, device := range devices {
			ip := net.ParseIP(device.IP)
			if subnet.Contains(ip) {
				deviceFqdn := fmt.Sprintf("%s.%s", makeDNSSafe(device.DnsName), dnsDomain)
				a := &dns.A{Hdr: dns.RR_Header{Name: deviceFqdn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A: net.ParseIP(device.IP)}
				records[dnsDomain].ARecords[deviceFqdn] = ARecord{
					record:    a,
					timestamp: timestamp,
				}

				ptrName := getPtrZoneFromIp(device.IP)
				ptr := &dns.PTR{Hdr: dns.RR_Header{Name: ptrName, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 60},
					Ptr: dns.Fqdn(deviceFqdn)}
				records[ptrZone].PtrRecords[ptrName] = PtrRecord{
					record:    ptr,
					timestamp: timestamp,
				}

			}
		}

		// process dhcp reservation records
		for _, reservation := range reservations {
			if !reservation.Status {
				continue
			}
			ip := net.ParseIP(reservation.IP)
			if ip == nil {
				continue
			}
			if subnet.Contains(ip) {
				dnsName := reservation.ClientName
				if reservation.ClientName == reservation.Mac && reservation.Description != "" {
					dnsName = reservation.Description
				}
				reservationFqdn := fmt.Sprintf("%s.%s", makeDNSSafeAllowWildcard(dnsName), dnsDomain)
				a := &dns.A{Hdr: dns.RR_Header{Name: reservationFqdn, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A: net.ParseIP(reservation.IP)}
				records[dnsDomain].ARecords[reservationFqdn] = ARecord{
					record:    a,
					timestamp: timestamp,
				}

				ptrName := getPtrZoneFromIp(reservation.IP)
				ptr := &dns.PTR{Hdr: dns.RR_Header{Name: ptrName, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 60},
					Ptr: dns.Fqdn(reservationFqdn)}
				records[ptrZone].PtrRecords[ptrName] = PtrRecord{
					record:    ptr,
					timestamp: timestamp,
				}
			}
		}

	}

	// add records to zone
	zones := make(map[string]*file.Zone)
	for dnsDomain, domainRecords := range records {
		_, ok := zones[dnsDomain]
		if !ok {
			log.Debugf("update: creating zone: %s", dnsDomain)
			zones[dnsDomain] = file.NewZone(dnsDomain, "")
			addSoaRecord(zones[dnsDomain], dnsDomain)
		}
		domainRecords.purgeStaleRecords(o.config.stale_record_duration.Seconds())

		// Add fallback record to zone if configured and applicable
		o.addFallbackRecord(zones, dnsDomain, domainRecords)

		for _, v := range domainRecords.ARecords {
			zones[dnsDomain].Insert(v.record)
		}
		for _, v := range domainRecords.PtrRecords {
			zones[ptrZone].Insert(v.record)
		}
		log.Debugf("update: zone %s contains %d records", dnsDomain, zones[dnsDomain].Count)
	}

	// get list of zone names
	zoneNames := make([]string, 0, len(zones))
	for k := range zones {
		zoneNames = append(zoneNames, k)
	}

	o.zMu.Lock()
	o.zones = zones
	o.zoneNames = zoneNames
	o.records = records
	o.zMu.Unlock()

	return nil
}

func getInterfaces(networks []omada.OmadaNetwork) (ret []omada.OmadaNetwork) {
	for _, network := range networks {
		match, _ := regexp.MatchString("interface", network.Purpose)
		if match {
			ret = append(ret, network)
		}
	}
	return
}

func (d *DnsRecords) purgeStaleRecords(maxAgeSeconds float64) {
	now := time.Now()
	for k, v := range d.ARecords {
		diff := now.Sub(v.timestamp)
		if diff.Seconds() > maxAgeSeconds {
			delete(d.ARecords, k)
			log.Debugf("purging stale record: %s", k)
		}
	}
	for k, v := range d.PtrRecords {
		diff := now.Sub(v.timestamp)
		if diff.Seconds() > maxAgeSeconds {
			delete(d.PtrRecords, k)
			log.Debugf("purging stale record: %s", k)
		}
	}
}

// addFallbackRecord adds wildcard fallback records for unresolved queries in each zone
func (o *Omada) addFallbackRecord(zones map[string]*file.Zone, dnsDomain string, domainRecords DnsRecords) {
	if o.config.fallback == "" {
		return // No fallback configured
	}

	log.Debugf("update: adding fallback %s record for zone: %s", o.config.fallback, dnsDomain)

	// Determine fallback IP based on configuration
	var fallbackIP net.IP

	// Case 1: fallback is an IP address
	if ip := net.ParseIP(o.config.fallback); ip != nil {
		fallbackIP = ip
		log.Debugf("update: fallback is IP: %s", fallbackIP.String())
	} else {
		// Case 2 & 3: fallback is FQDN or hostname
		var targetFQDN string

		// Check if it contains dots (FQDN) or is just a hostname
		if strings.Contains(o.config.fallback, ".") {
			// Case 2: FQDN - use as-is
			targetFQDN = dns.Fqdn(o.config.fallback)
		} else {
			// Case 3: hostname - append current zone
			targetFQDN = fmt.Sprintf("%s.%s", o.config.fallback, dnsDomain)
		}

		log.Debugf("update: looking for fallback FQDN: %s", targetFQDN)

		// Search all zones for the target FQDN
		fallbackIP = o.resolveInZones(targetFQDN)
		if fallbackIP == nil {
			log.Warningf("update: fallback '%s' not found in any zone, skipping fallback for zone %s",
				o.config.fallback, dnsDomain)
			return
		}

		log.Debugf("update: found fallback IP: %s for FQDN: %s", fallbackIP.String(), targetFQDN)
	}

	// Create wildcard A record for this zone pointing to the fallback IP
	wildcardName := fmt.Sprintf("*.%s", dnsDomain)
	fallbackRecord := &dns.A{
		Hdr: dns.RR_Header{
			Name:   wildcardName,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    300,
		},
		A: fallbackIP,
	}
	zones[dnsDomain].Insert(fallbackRecord)
	log.Debugf("update: added wildcard fallback record: %s -> %s", wildcardName, fallbackIP.String())
}

// resolveInZones searches all current records for an FQDN and returns its IP
func (o *Omada) resolveInZones(targetFQDN string) net.IP {
	// Search in current records map
	for _, domainRecords := range o.records {
		for fqdn, aRecord := range domainRecords.ARecords {
			if fqdn == targetFQDN {
				return aRecord.record.A
			}
		}
	}
	return nil
}
