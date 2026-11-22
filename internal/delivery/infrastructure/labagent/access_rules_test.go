package labagent

import (
	"testing"

	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"
	"github.com/stretchr/testify/require"

	exerciseModel "github.com/cybericebox/backend/internal/model/exercise"
	labAccessModel "github.com/cybericebox/backend/internal/model/labAccess"
)

func TestVPNGatewaysAreReadFromTheSpecOfALab(t *testing.T) {
	spec := []byte(`{"gateways":[{"name":"vpn","kind":"vpn"},{"name":"internet","kind":"internet"},{"name":"b-vpn","kind":"vpn"}],"devices":[]}`)
	require.Equal(t, []string{"b-vpn", "vpn"}, vpnGatewaysOfSpec(spec))
	require.Empty(t, vpnGatewaysOfSpec([]byte(`{"devices":[]}`)))
	require.Empty(t, vpnGatewaysOfSpec([]byte(`not json`)))
}

func TestAnAllowRuleNamesTheClientTheLabsAndTheirVPNGateways(t *testing.T) {
	gateways := map[string][]string{
		"lab-a": {"vpn"}, "lab-b": {"vpn"}, // one rule: the same connector
		"lab-c":  {"entry", "vpn"}, // another set: another rule
		"lab-no": {},               // a lab without a VPN gateway has nothing to grant
	}
	rules := accessRules([]labAccessModel.ClientPolicy{
		{Name: "p-1", AllowedLabs: []string{"lab-a", "lab-no", "lab-b", "lab-c"}},
		{Name: "p-empty"},
		{Name: "p-novpn", AllowedLabs: []string{"lab-no"}},
	}, gateways)
	require.Len(t, rules, 2, "p-empty and p-novpn get no rule: default deny")
	for _, rule := range rules {
		require.Equal(t, labpb.LabGroupAccessAction_LAB_GROUP_ACCESS_ACTION_ALLOW, rule.GetAction())
		require.Equal(t, []string{"p-1"}, rule.GetClientNames())
		require.Empty(t, rule.GetInclude(), "the whole segment unless a caller narrows it")
		require.NotEmpty(t, rule.GetGatewayNames(), "an allow without gateway names opens no VPN")
	}
	byGateways := map[string][]string{}
	for _, rule := range rules {
		byGateways[rule.GetGatewayNames()[0]] = rule.GetLabNames()
	}
	require.Equal(t, []string{"lab-c"}, byGateways["entry"])
	require.Equal(t, []string{"lab-a", "lab-b"}, byGateways["vpn"])
	for _, rule := range rules {
		if rule.GetGatewayNames()[0] == "entry" {
			require.Equal(t, []string{"entry", "vpn"}, rule.GetGatewayNames())
		}
	}
}

func TestALabTheAgentDoesNotListGetsTheDefaultGateway(t *testing.T) {
	rules := accessRules([]labAccessModel.ClientPolicy{{Name: "p-1", AllowedLabs: []string{"lab-new"}}}, map[string][]string{})
	require.Len(t, rules, 1)
	require.Equal(t, []string{"vpn"}, rules[0].GetGatewayNames())
	require.Equal(t, []string{"lab-new"}, rules[0].GetLabNames())
}

func TestTheRulesComeInAFixedOrder(t *testing.T) {
	policies := []labAccessModel.ClientPolicy{{Name: "p-1", AllowedLabs: []string{"l1", "l2", "l3"}}}
	gateways := map[string][]string{"l1": {"z"}, "l2": {"a"}, "l3": {"m"}}
	first := accessRules(policies, gateways)
	for i := 0; i < 20; i++ {
		again := accessRules(policies, gateways)
		for j := range first {
			require.Equal(t, first[j].GetGatewayNames(), again[j].GetGatewayNames())
		}
	}
	require.Equal(t, []string{"a"}, first[0].GetGatewayNames())
}

func TestTheControlGatewayIsNeverAParticipantsGateway(t *testing.T) {
	spec := []byte(`{"gateways":[{"name":"vpn","kind":"vpn"},{"name":"ctl","kind":"vpn"},{"name":"ctl-2","kind":"vpn"},{"name":"ctl-8","kind":"vpn"}]}`)
	require.Equal(t, []string{"vpn"}, vpnGatewaysOfSpec(spec))
	rules := accessRules([]labAccessModel.ClientPolicy{{Name: "p-1", AllowedLabs: []string{"lab-bot"}}}, map[string][]string{"lab-bot": vpnGatewaysOfSpec(spec)})
	require.Len(t, rules, 1)
	require.Equal(t, []string{"vpn"}, rules[0].GetGatewayNames(), "a participant's rule never names ctl")
}

func TestTheClientOfABotGetsOneAddressOfTheControlGatewayAndNothingElse(t *testing.T) {
	rules := accessRules([]labAccessModel.ClientPolicy{
		{Name: "g-run", Control: &labAccessModel.ControlAccess{Lab: "lab-bot", Gateway: exerciseModel.ControlGatewayOf(0), Host: exerciseModel.ControlHostBase}},
		{Name: "p-1", AllowedLabs: []string{"lab-bot"}},
	}, map[string][]string{"lab-bot": {"vpn"}})
	require.Len(t, rules, 2)
	bot := rules[0]
	require.Equal(t, []string{"g-run"}, bot.GetClientNames())
	require.Equal(t, []string{"lab-bot"}, bot.GetLabNames())
	require.Equal(t, []string{"ctl"}, bot.GetGatewayNames())
	require.Len(t, bot.GetInclude(), 1)
	require.Equal(t, int32(10), bot.GetInclude()[0].GetHost())
	require.Equal(t, int32(32), bot.GetInclude()[0].GetPrefix())
	require.Equal(t, labpb.LabGroupAccessAction_LAB_GROUP_ACCESS_ACTION_ALLOW, bot.GetAction())
	for _, rule := range rules[1:] {
		require.NotContains(t, rule.GetGatewayNames(), "ctl")
		require.Equal(t, []string{"p-1"}, rule.GetClientNames())
	}
}

// The bot of the second tools device of a lab reaches .10 of that device's own control gateway, never the first one's.
func TestTheClientOfASecondBotNamesItsOwnControlGateway(t *testing.T) {
	rules := accessRules([]labAccessModel.ClientPolicy{
		{Name: "g-one", Control: &labAccessModel.ControlAccess{Lab: "lab-bot", Gateway: exerciseModel.ControlGatewayOf(0), Host: exerciseModel.ControlHostBase}},
		{Name: "g-two", Control: &labAccessModel.ControlAccess{Lab: "lab-bot", Gateway: exerciseModel.ControlGatewayOf(1), Host: exerciseModel.ControlHostBase}},
	}, map[string][]string{"lab-bot": {"vpn"}})
	require.Len(t, rules, 2)
	require.Equal(t, []string{"ctl"}, rules[0].GetGatewayNames())
	require.Equal(t, []string{"ctl-2"}, rules[1].GetGatewayNames())
	require.Equal(t, []string{"g-two"}, rules[1].GetClientNames())
	require.Equal(t, int32(10), rules[1].GetInclude()[0].GetHost())
}

func TestAGradersRuleNamesEveryVPNGatewayAParticipantGetsAndNeverCtl(t *testing.T) {
	spec := []byte(`{"gateways":[{"name":"vpn","kind":"vpn"},{"name":"vpn-b","kind":"vpn"},{"name":"internet","kind":"internet"},{"name":"ctl","kind":"vpn"},{"name":"ctl-3","kind":"vpn"}]}`)
	gateways := map[string][]string{"lab-run": vpnGatewaysOfSpec(spec)}
	rules := accessRules([]labAccessModel.ClientPolicy{
		{Name: "g-grader", AllowedLabs: []string{"lab-run"}},
		{Name: "p-participant", AllowedLabs: []string{"lab-run"}},
	}, gateways)
	require.Len(t, rules, 2)
	for _, rule := range rules {
		require.Equal(t, []string{"vpn", "vpn-b"}, rule.GetGatewayNames(), "every VPN gateway of the lab a participant gets, never ctl or the internet")
		require.Empty(t, rule.GetInclude())
	}
}
