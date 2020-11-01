package realip

import (
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type module struct {
	From   []*net.IPNet
	Header string

	// MaxHops configures the maxiumum number of hops or IPs to be found in a forward header.
	// It's purpose is to prevent abuse and/or DOS attacks from long forward-chains, since each one
	// must be parsed and checked against a list of subnets.
	// The default is 5, -1 to disable. If set to 0, any request with a forward header will be rejected
	MaxHops int
	Strict  bool
}

var presets = map[string][]string{
	// from https://www.cloudflare.com/ips/
	"cloudflare": {
		"103.21.244.0/22",
		"103.22.200.0/22",
		"103.31.4.0/22",
		"104.16.0.0/12",
		"108.162.192.0/18",
		"131.0.72.0/22",
		"141.101.64.0/18",
		"162.158.0.0/15",
		"172.64.0.0/13",
		"173.245.48.0/20",
		"188.114.96.0/20",
		"190.93.240.0/20",
		"197.234.240.0/22",
		"198.41.128.0/17",
		"2400:cb00::/32",
		"2405:8100::/32",
		"2405:b500::/32",
		"2606:4700::/32",
		"2803:f800::/32",
		"2c0f:f248::/32",
		"2a06:98c0::/29",
	},
	// https://cloud.google.com/compute/docs/load-balancing/http/#firewall_rules
	"gcp": {
		"130.211.0.0/22",
		"35.191.0.0/16",
	},
	// https://support.rackspace.com/how-to/using-cloud-load-balancers-with-rackconnect/
	"rackspace": {
		// DFW region
		"10.189.254.0/24",
		"10.189.252.0/24",
		"10.183.248.0/24",
		"10.187.186.0/24",
		"10.183.250.0/24",
		// IAD region
		"10.187.191.0/24",
		"10.189.255.0/24",
		"10.187.186.0/24",
		"10.189.254.0/24",
		// ORD region
		"10.183.253.0/24",
		"10.183.250.0/24",
		"10.189.246.0/24",
		"10.187.187.0/24",
		"10.187.186.0/24",
		"10.183.252.0/24",
		"10.189.245.0/24",
		"10.183.251.0/24",
		// LON region
		"10.187.191.0/24",
		"10.190.254.0/24",
		"10.189.246.0/24",
		"10.190.255.0/24",
		"10.187.190.0/24",
		"10.189.247.0/24",
		// SYD region
		"10.189.254.0/24",
		// HKG region
		"10.189.254.0/24",
	},
}

func init() {
	caddy.RegisterModule(module{})
	httpcaddyfile.RegisterHandlerDirective("realip", parseCaddyfileHandler)
}

func (module) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.handlers.realip",
		New: func() caddy.Module {
			return new(module)
		},
	}
}

func (m *module) Provision(ctx caddy.Context) error {

	return nil
}

func parseCaddyfileHandler(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m module
	err := m.UnmarshalCaddyfile(h.Dispenser)
	if err != nil {
		return nil, err
	}
	return m, err
}

// Adds a list of CIDR IP Ranges to the From whitelist
func addIpRanges(m *module, d *caddyfile.Dispenser, ranges []string) error {
	for _, v := range ranges {
		if preset, ok := presets[v]; ok {
			if err := addIpRanges(m, d, preset); err != nil {
				return err
			}
			continue
		}
		_, cidr, err := net.ParseCIDR(v)
		if err != nil {
			return d.Err(err.Error())
		}
		m.From = append(m.From, cidr)
	}
	return nil
}

func parseStringArg(d *caddyfile.Dispenser, out *string) error {
	if !d.Args(out) {
		return d.ArgErr()
	}
	return nil
}

func parseIntArg(d *caddyfile.Dispenser, out *int) error {
	var strVal string
	err := parseStringArg(d, &strVal)
	if err == nil {
		*out, err = strconv.ParseInt(strVal)
	}
	return err
}

func parseBoolArg(d *caddyfile.Dispenser, out *bool) error {
	var strVal string
	err := parseStringArg(d, &strVal)
	if err == nil {
		*out, err = strconv.ParseBool(strVal)
	}
	return err
}

func (m *module) validSource(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, from := range m.From {
		if from.Contains(ip) {
			return true
		}
	}
	return false
}

func (m module) ServeHTTP(w http.ResponseWriter, req *http.Request, handler caddyhttp.Handler) error {
	host, port, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil || !m.validSource(host) {
		if m.Strict {
			return fmt.Errorf("Error reading remote addr: %s", req.RemoteAddr)
		}
		return handler.ServeHTTP(w, req) // Change nothing and let next deal with it.
	}
	if !m.validSource(host) {
		if m.Strict {
			return fmt.Errorf("Unrecognized proxy ip address: %s", host)
		}
		return handler.ServeHTTP(w, req)
	}

	if hVal := req.Header.Get(m.Header); hVal != "" {
		//restore original host:port format
		parts := strings.Split(hVal, ",")
		for i, part := range parts {
			parts[i] = strings.TrimSpace(part)
		}
		if m.MaxHops != -1 && len(parts) > m.MaxHops {
			return fmt.Errorf("Too many forward addresses")
		}
		ip := net.ParseIP(parts[len(parts)-1])
		if ip == nil {
			if m.Strict {
				return fmt.Errorf("Unrecognized proxy ip address: %s", parts[len(parts)-1])
			}
			return handler.ServeHTTP(w, req)
		}
		req.RemoteAddr = net.JoinHostPort(parts[len(parts)-1], port)
		for i := len(parts) - 1; i >= 0; i-- {
			req.RemoteAddr = net.JoinHostPort(parts[i], port)
			if i > 0 && !m.validSource(parts[i]) {
				if m.Strict {
					return fmt.Errorf("Unrecognized proxy ip address: %s", parts[i])
				}
				return handler.ServeHTTP(w, req)
			}
		}
	}
	return handler.ServeHTTP(w, req)
}

func (m *module) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.NextArg()
	
	for d.Next(0) {

/*
		if m != nil {
			return d.Err("cannot specify realip more than once")
		}
	
		m = &module{
			Header:  "X-Forwarded-For",
			MaxHops: 5,
		}
*/
	/*	for nesting := d.Nesting(); d.NextBlock(nesting); {
			
			args := d.RemainingArgs()
			
			if len(args) > 0 {
				if err := addIpRanges(m, d, args); err != nil {
					return err
				}
			}
	*/		
			var err error
		
			switch d.Val() {
			case "header":
				err = parseStringArg(d, &m.Header)
			case "from":
				err = addIpRanges(m, d, d.RemainingArgs())
			case "strict":
				err = parseBoolArg(d, &m.Strict)
			case "maxhops":
				err = parseIntArg(d, &m.MaxHops)
			default:
				return d.Errf("Unknown realip arg")
			}
			if err != nil {
				return d.Errf("Error parsing %s: %s", d.Val(), err)
			}
		}
	return nil
}



var (
	_ caddy.Provisioner           = (*module)(nil)
	_ caddyhttp.MiddlewareHandler = (*module)(nil)
	_ caddyfile.Unmarshaler       = (*module)(nil)
)

