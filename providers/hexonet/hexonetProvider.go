// Package hexonet implements a registrar that uses the hexonet api to set name servers. It will self register it's providers when imported.
package hexonet

import (
	"encoding/json"
	"errors"

	"github.com/StackExchange/dnscontrol/v4/pkg/version"
	"github.com/StackExchange/dnscontrol/v4/providers"
	hxcl "github.com/centralnicgroup-opensource/rtldev-middleware-go-sdk/v4/apiclient"
)

// HXClient describes a connection to the hexonet API.
type HXClient struct {
	APILogin    string
	APIPassword string
	APIEntity   string
	client      *hxcl.APIClient
}

var features = providers.DocumentationNotes{
	// The default for unlisted capabilities is 'Cannot'.
	// See providers/capabilities.go for the entire list of capabilities.
	providers.CanGetZones:            providers.Unimplemented(),
	providers.CanConcur:              providers.Unimplemented(),
	providers.CanUseAlias:            providers.Cannot("Using ALIAS is possible through our extended DNS (X-DNS) service. Feel free to get in touch with us."),
	providers.CanUseCAA:              providers.Can(),
	providers.CanUseLOC:              providers.Unimplemented(),
	providers.CanUsePTR:              providers.Can(),
	providers.CanUseSRV:              providers.Can("SRV records with empty targets are not supported"),
	providers.CanUseTLSA:             providers.Can(),
	providers.DocCreateDomains:       providers.Can(),
	providers.DocDualHost:            providers.Can(),
	providers.DocOfficiallySupported: providers.Cannot("Actively maintained provider module."),
}

func newProvider(conf map[string]string) (*HXClient, error) {
	api := &HXClient{
		client: hxcl.NewAPIClient(),
	}
	api.client.SetUserAgent("DNSControl", version.Version())
	api.APILogin, api.APIPassword, api.APIEntity = conf["apilogin"], conf["apipassword"], conf["apientity"]
	if conf["debugmode"] == "1" {
		api.client.EnableDebugMode()
	}
	if len(conf["ipaddress"]) > 0 {
		api.client.SetRemoteIPAddress(conf["ipaddress"])
	}
	if api.APIEntity != "OTE" && api.APIEntity != "LIVE" {
		return nil, errors.New("wrong api system entity used. use \"OTE\" for OT&E system or \"LIVE\" for Live system")
	}
	if api.APIEntity == "OTE" {
		api.client.UseOTESystem()
	}
	if api.APILogin == "" || api.APIPassword == "" {
		return nil, errors.New("missing login credentials apilogin or apipassword")
	}
	api.client.SetCredentials(api.APILogin, api.APIPassword)
	return api, nil
}

func newReg(conf map[string]string) (providers.Registrar, error) {
	return newProvider(conf)
}

func newDsp(conf map[string]string, meta json.RawMessage) (providers.DNSServiceProvider, error) {
	return newProvider(conf)
}

func init() {
	const providerName = "HEXONET"
	const providerMaintainer = "@KaiSchwarz-cnic"
	fns := providers.DspFuncs{
		Initializer:   newDsp,
		RecordAuditor: AuditRecords,
	}
	providers.RegisterRegistrarType(providerName, newReg)
	providers.RegisterDomainServiceProviderType(providerName, fns, features)
	providers.RegisterMaintainer(providerName, providerMaintainer)
}
