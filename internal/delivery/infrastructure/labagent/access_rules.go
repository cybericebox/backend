package labagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	labAccessModel "github.com/cybericebox/backend/internal/model/labAccess"
)

// defaultVPNGateway is the name the daemon gives the VPN gateway of every lab it builds (see buildGateway). It stands for a lab
// the agent does not list yet: the agent then answers that the lab is not in the group, as it always did, and the policy is
// tried again.
const defaultVPNGateway = "vpn"

// vpnGateways reads, for the labs of a group, the names of their VPN gateways from the specs the agent holds. A lab without a
// VPN gateway is present with an empty list: there is nothing to grant on it. A lab that is not listed is absent.
func (c *Client) vpnGateways(ctx context.Context, group string) (map[string][]string, error) {
	list, err := c.ListLabs(ctx, &labpb.ListRequest{LabGroup: group})
	if err != nil {
		return nil, fmt.Errorf("list labs of group %q: %w", group, err)
	}
	out := make(map[string][]string, len(list.GetItems()))
	for _, l := range list.GetItems() {
		out[l.GetName()] = vpnGatewaysOfSpec(l.GetSpecJson())
	}
	return out, nil
}

// labHasVPNConnector says the lab's spec has a VPN gateway of any kind of use, the hidden control gateway of a tools device
// included (it is a VPN connector too).
func labHasVPNConnector(specJSON []byte) bool {
	var spec struct {
		Gateways []struct {
			Kind string `json:"kind"`
		} `json:"gateways"`
	}
	if json.Unmarshal(specJSON, &spec) != nil {
		return true // unreadable: the stricter reading, a boot id is needed
	}
	for _, g := range spec.Gateways {
		if g.Kind == "vpn" {
			return true
		}
	}
	return false
}

func vpnGatewaysOfSpec(specJSON []byte) []string {
	var spec struct {
		Gateways []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"gateways"`
	}
	if json.Unmarshal(specJSON, &spec) != nil {
		return nil
	}
	var out []string
	for _, g := range spec.Gateways {
		// A control gateway of a tools device is never a participant's: only the run client of that bot gets a rule for it.
		if g.Kind == "vpn" && !exerciseModel.IsControlGatewayName(g.Name) {
			out = append(out, g.Name)
		}
	}
	sort.Strings(out)
	return out
}

// accessRules turns the desired access of the clients into the rules of the group policy (contract 2.4): an allow rule names
// the client, the laboratories and the VPN gateways of those laboratories, so a client gets exactly the connectors of the labs it
// is allowed. The labs of one client that have the same gateways share one rule; the include list stays empty (the whole
// segment) except for the client of a bot, which gets one /32 of the control gateway of its own tools device. A lab whose spec has no VPN gateway gets no rule (it has no VPN access to give); a lab the agent does not list gets
// the default gateway name, so the agent still answers that it is not in the group. The rules come in a fixed order.
func accessRules(policies []labAccessModel.ClientPolicy, gateways map[string][]string) []*labpb.LabGroupAccessRule {
	rules := make([]*labpb.LabGroupAccessRule, 0, len(policies))
	for _, policy := range policies {
		if c := policy.Control; c != nil {
			// The client of a bot: one address of the control gateway of its own tools device in one lab, nothing else.
			gateway := c.Gateway
			if gateway == "" {
				gateway = exerciseModel.ControlGatewayName
			}
			rules = append(rules, &labpb.LabGroupAccessRule{
				Action:       labpb.LabGroupAccessAction_LAB_GROUP_ACCESS_ACTION_ALLOW,
				ClientNames:  []string{policy.Name},
				LabNames:     []string{c.Lab},
				GatewayNames: []string{gateway},
				Include:      []*labpb.AccessInclude{{Host: c.Host, Prefix: 32}},
			})
		}
		// An empty LabNames list means "all Labs" in the operator protocol (and an allow rule must name its laboratories).
		// Omit the rule instead so the group's default-deny policy revokes the client when no challenge Lab is available.
		byGateways := map[string][]string{}
		for _, lab := range policy.AllowedLabs {
			names, listed := gateways[lab]
			if !listed {
				names = []string{defaultVPNGateway}
			}
			if len(names) == 0 {
				continue
			}
			key := strings.Join(names, "\x00")
			byGateways[key] = append(byGateways[key], lab)
		}
		keys := make([]string, 0, len(byGateways))
		for key := range byGateways {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			rules = append(rules, &labpb.LabGroupAccessRule{
				Action:       labpb.LabGroupAccessAction_LAB_GROUP_ACCESS_ACTION_ALLOW,
				ClientNames:  []string{policy.Name},
				LabNames:     append([]string(nil), byGateways[key]...),
				GatewayNames: strings.Split(key, "\x00"),
			})
		}
	}
	return rules
}
