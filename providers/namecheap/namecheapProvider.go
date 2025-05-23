package namecheap

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/StackExchange/dnscontrol/v4/models"
	"github.com/StackExchange/dnscontrol/v4/pkg/diff"
	"github.com/StackExchange/dnscontrol/v4/pkg/printer"
	"github.com/StackExchange/dnscontrol/v4/providers"
	nc "github.com/billputer/go-namecheap"
	"golang.org/x/net/publicsuffix"
)

// NamecheapDefaultNs lists the default nameservers for this provider.
var NamecheapDefaultNs = []string{"dns1.registrar-servers.com", "dns2.registrar-servers.com"}

// namecheapProvider is the handle for this provider.
type namecheapProvider struct {
	APIKEY  string
	APIUser string
	client  *nc.Client
}

var features = providers.DocumentationNotes{
	// The default for unlisted capabilities is 'Cannot'.
	// See providers/capabilities.go for the entire list of capabilities.
	providers.CanGetZones:            providers.Can(),
	providers.CanConcur:              providers.Can(),
	providers.CanUseAlias:            providers.Can(),
	providers.CanUseCAA:              providers.Can(),
	providers.CanUseLOC:              providers.Cannot(),
	providers.CanUsePTR:              providers.Cannot(),
	providers.CanUseSRV:              providers.Cannot("The namecheap web console allows you to make SRV records, but their api does not let you read or set them"),
	providers.CanUseTLSA:             providers.Cannot(),
	providers.DocCreateDomains:       providers.Cannot("Requires domain registered through their service"),
	providers.DocDualHost:            providers.Cannot("Doesn't allow control of apex NS records"),
	providers.DocOfficiallySupported: providers.Cannot(),
}

func init() {
	const providerName = "NAMECHEAP"
	const providerMaintainer = "@willpower232"
	providers.RegisterRegistrarType(providerName, newReg)
	fns := providers.DspFuncs{
		Initializer:   newDsp,
		RecordAuditor: AuditRecords,
	}
	providers.RegisterDomainServiceProviderType(providerName, fns, features)
	providers.RegisterCustomRecordType("URL", providerName, "")
	providers.RegisterCustomRecordType("URL301", providerName, "")
	providers.RegisterCustomRecordType("FRAME", providerName, "")
	providers.RegisterMaintainer(providerName, providerMaintainer)
}

func newDsp(conf map[string]string, metadata json.RawMessage) (providers.DNSServiceProvider, error) {
	return newProvider(conf, metadata)
}

func newReg(conf map[string]string) (providers.Registrar, error) {
	return newProvider(conf, nil)
}

func newProvider(m map[string]string, _ json.RawMessage) (*namecheapProvider, error) {
	api := &namecheapProvider{}
	api.APIUser, api.APIKEY = m["apiuser"], m["apikey"]
	if api.APIKEY == "" || api.APIUser == "" {
		return nil, errors.New("missing Namecheap apikey and apiuser")
	}
	api.client = nc.NewClient(api.APIUser, api.APIKEY, api.APIUser)
	// if BaseURL is specified in creds, use that url
	BaseURL, ok := m["BaseURL"]
	if ok {
		api.client.BaseURL = BaseURL
	}
	return api, nil
}

func splitDomain(domain string) (sld string, tld string) {
	tld, _ = publicsuffix.PublicSuffix(domain)
	d, _ := publicsuffix.EffectiveTLDPlusOne(domain)
	sld = strings.Split(d, ".")[0]
	return sld, tld
}

// namecheap has request limiting at unpublished limits
// from support in SEP-2017:
//
//	"The limits for the API calls will be 20/Min, 700/Hour and 8000/Day for one user.
//	 If you can limit the requests within these it should be fine."
//
// this helper performs some api action, checks for rate limited response, and if so, enters a retry loop until it resolves
// if you are consistently hitting this, you may have success asking their support to increase your account's limits.
func doWithRetry(f func() error) {
	// sleep 5 seconds at a time, up to 23 times (1 minute, 15 seconds)
	const maxRetries = 23
	const sleepTime = 5 * time.Second
	var currentRetry int
	for {
		err := f()
		if err == nil {
			return
		}
		if strings.Contains(err.Error(), "unexpected status code from api: 405") {
			currentRetry++
			if currentRetry >= maxRetries {
				return
			}
			printer.Printf("Namecheap rate limit exceeded. Waiting %s to retry.\n", sleepTime)
			time.Sleep(sleepTime)
		} else {
			return
		}
	}
}

// GetZoneRecords gets the records of a zone and returns them in RecordConfig format.
func (n *namecheapProvider) GetZoneRecords(domain string, meta map[string]string) (models.Records, error) {
	sld, tld := splitDomain(domain)
	var records *nc.DomainDNSGetHostsResult
	var err error
	doWithRetry(func() error {
		records, err = n.client.DomainsDNSGetHosts(sld, tld)
		return err
	})
	if err != nil {
		return nil, err
	}

	// namecheap has this really annoying feature where they add some parking records if you have no records.
	// This causes a few problems for our purposes, specifically the integration tests.
	// lets detect that one case and pretend it is a no-op.
	if len(records.Hosts) == 2 {
		if records.Hosts[0].Type == "CNAME" &&
			strings.Contains(records.Hosts[0].Address, "parkingpage") &&
			records.Hosts[1].Type == "URL" {
			// return an empty zone
			return nil, nil
		}
	}

	// Copying this from GetDomainCorrections.  This seems redundant
	// with what toRecords() does.  Leaving it out.
	// 	for _, r := range records.Hosts {
	// 		if r.Type == "SOA" {
	// 			continue
	// 		}
	// 		rec := &models.RecordConfig{
	// 			Type:         r.Type,
	// 			TTL:          uint32(r.TTL),
	// 			MxPreference: uint16(r.MXPref),
	// 			Original:     r,
	// 		}
	// 		rec.SetLabel(r.Name, dc.Name)
	// 		switch rtype := r.Type; rtype { // #rtype_variations
	// 		case "TXT":
	// 			rec.SetTargetTXT(r.Address)
	// 		case "CAA":
	// 			rec.SetTargetCAAString(r.Address)
	// 		default:
	// 			rec.SetTarget(r.Address)
	// 		}
	// 		actual = append(actual, rec)
	// 	}

	return toRecords(records, domain)
}

// // GetDomainCorrections returns the corrections for the domain.
// func (n *namecheapProvider) GetDomainCorrections(dc *models.DomainConfig) ([]*models.Correction, error) {
// 	dc.Punycode()
// 	sld, tld := splitDomain(dc.Name)
// 	var records *nc.DomainDNSGetHostsResult
// 	var err error
// 	doWithRetry(func() error {
// 		records, err = n.client.DomainsDNSGetHosts(sld, tld)
// 		return err
// 	})
// 	if err != nil {
// 		return nil, err
// 	}

// 	var actual []*models.RecordConfig

// 	// namecheap does not allow setting @ NS with basic DNS
// 	dc.Filter(func(r *models.RecordConfig) bool {
// 		if r.Type == "NS" && r.GetLabel() == "@" {
// 			if !strings.HasSuffix(r.GetTargetField(), "registrar-servers.com.") {
// 				printer.Println("\n", r.GetTargetField(), "Namecheap does not support changing apex NS records. Skipping.")
// 			}
// 			return false
// 		}
// 		return true
// 	})

// 	// namecheap has this really annoying feature where they add some parking records if you have no records.
// 	// This causes a few problems for our purposes, specifically the integration tests.
// 	// lets detect that one case and pretend it is a no-op.
// 	if len(dc.Records) == 0 && len(records.Hosts) == 2 {
// 		if records.Hosts[0].Type == "CNAME" &&
// 			strings.Contains(records.Hosts[0].Address, "parkingpage") &&
// 			records.Hosts[1].Type == "URL" {
// 			return nil, nil
// 		}
// 	}

// 	for _, r := range records.Hosts {
// 		if r.Type == "SOA" {
// 			continue
// 		}
// 		rec := &models.RecordConfig{
// 			Type:         r.Type,
// 			TTL:          uint32(r.TTL),
// 			MxPreference: uint16(r.MXPref),
// 			Original:     r,
// 		}
// 		rec.SetLabel(r.Name, dc.Name)
// 		switch rtype := r.Type; rtype { // #rtype_variations
// 		case "TXT":
// 			rec.SetTargetTXT(r.Address)
// 		case "CAA":
// 			rec.SetTargetCAAString(r.Address)
// 		default:
// 			rec.SetTarget(r.Address)
// 		}
// 		actual = append(actual, rec)
// 	}

// 	// Normalize
// 	models.PostProcessRecords(actual)

// 	return n.GetZoneRecordsCorrections(dc, actual)
// }

// GetZoneRecordsCorrections returns a list of corrections that will turn existing records into dc.Records.
func (n *namecheapProvider) GetZoneRecordsCorrections(dc *models.DomainConfig, actual models.Records) ([]*models.Correction, int, error) {
	// namecheap does not allow setting @ NS with basic DNS
	dc.Filter(func(r *models.RecordConfig) bool {
		if r.Type == "NS" && r.GetLabel() == "@" {
			if !strings.HasSuffix(r.GetTargetField(), "registrar-servers.com.") {
				printer.Println("\n", r.GetTargetField(), "Namecheap does not support changing apex NS records. Skipping.")
			}
			return false
		}
		return true
	})

	toReport, toCreate, toDelete, toModify, actualChangeCount, err := diff.NewCompat(dc).IncrementalDiff(actual)
	if err != nil {
		return nil, 0, err
	}
	// Start corrections with the reports
	corrections := diff.GenerateMessageCorrections(toReport)

	// because namecheap doesn't have selective create, delete, modify,
	// we bundle them all up to send at once.  We *do* want to see the
	// changes though

	var desc []string
	for _, i := range toCreate {
		desc = append(desc, "\n"+i.String())
	}
	for _, i := range toDelete {
		desc = append(desc, "\n"+i.String())
	}
	for _, i := range toModify {
		desc = append(desc, "\n"+i.String())
	}

	// only create corrections if there are changes
	if len(desc) > 0 {
		msg := fmt.Sprintf("GENERATE_ZONE: %s (%d records)%s", dc.Name, len(dc.Records), desc)
		corrections = append(corrections,
			&models.Correction{
				Msg: msg,
				F: func() error {
					return n.generateRecords(dc)
				},
			})
	}

	return corrections, actualChangeCount, nil
}

func toRecords(result *nc.DomainDNSGetHostsResult, origin string) ([]*models.RecordConfig, error) {
	var records []*models.RecordConfig
	for _, dnsHost := range result.Hosts {
		record := models.RecordConfig{
			Type:         dnsHost.Type,
			TTL:          uint32(dnsHost.TTL),
			MxPreference: uint16(dnsHost.MXPref),
			Name:         dnsHost.Name,
		}
		record.SetLabel(dnsHost.Name, origin)

		var err error
		switch dnsHost.Type {
		case "MX":
			err = record.SetTargetMX(uint16(dnsHost.MXPref), dnsHost.Address)
		case "FRAME", "URL", "URL301":
			err = record.SetTarget(dnsHost.Address)
		default:
			err = record.PopulateFromString(dnsHost.Type, dnsHost.Address, origin)
		}
		if err != nil {
			return nil, err
		}

		records = append(records, &record)
	}

	return records, nil
}

func (n *namecheapProvider) generateRecords(dc *models.DomainConfig) error {
	var recs []nc.DomainDNSHost

	id := 1
	for _, r := range dc.Records {
		var value string
		switch rtype := r.Type; rtype { // #rtype_variations
		case "CAA":
			value = r.GetTargetCombined()
		default:
			value = r.GetTargetField()
		}

		rec := nc.DomainDNSHost{
			ID:      id,
			Name:    r.GetLabel(),
			Type:    r.Type,
			Address: value,
			MXPref:  int(r.MxPreference),
			TTL:     int(r.TTL),
		}
		recs = append(recs, rec)
		id++
	}
	sld, tld := splitDomain(dc.Name)
	var err error
	doWithRetry(func() error {
		_, err = n.client.DomainDNSSetHosts(sld, tld, recs)
		return err
	})
	return err
}

// GetNameservers returns the nameservers for a domain.
func (n *namecheapProvider) GetNameservers(domainName string) ([]*models.Nameserver, error) {
	// return default namecheap nameservers
	return models.ToNameservers(NamecheapDefaultNs)
}

// GetRegistrarCorrections returns corrections to update nameservers.
func (n *namecheapProvider) GetRegistrarCorrections(dc *models.DomainConfig) ([]*models.Correction, error) {
	var info *nc.DomainInfo
	var err error
	doWithRetry(func() error {
		info, err = n.client.DomainGetInfo(dc.Name)
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(info.DNSDetails.Nameservers)
	found := strings.Join(info.DNSDetails.Nameservers, ",")
	desiredNs := []string{}
	for _, d := range dc.Nameservers {
		desiredNs = append(desiredNs, d.Name)
	}
	sort.Strings(desiredNs)
	desired := strings.Join(desiredNs, ",")
	if found != desired {
		parts := strings.SplitN(dc.Name, ".", 2)
		sld, tld := parts[0], parts[1]
		return []*models.Correction{
			{
				Msg: fmt.Sprintf("Change Nameservers from '%s' to '%s'", found, desired),
				F: func() (err error) {
					doWithRetry(func() error {
						_, err = n.client.DomainDNSSetCustom(sld, tld, desired)
						return err
					})
					return
				},
			},
		}, nil
	}
	return nil, nil
}
