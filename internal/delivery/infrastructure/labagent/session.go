package labagent

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	labclient "github.com/cybericebox/laboratory/pkg/agent/client"

	infraModel "github.com/cybericebox/backend/internal/model/infrastructure"
	"github.com/cybericebox/backend/pkg/labaccess"
)

// SessionIssuer signs the access grants of the laboratory (the web and console sessions) with the access key
// of the agent that holds the lab group: every agent is a tenant with its own key, the tenant name is the
// grant's issuer. A group whose agent has no usable key, or whose agent speaks another major contract than
// this build (it would not accept a grant), gets no link (VPN access is unaffected).
type SessionIssuer struct {
	Fleet  *Fleet
	Issuer *labaccess.Issuer
}

// warnLimits says once per agent that the configured grant length is above what its proxy accepts; grants are capped.
func (s SessionIssuer) warnLimits(member *Member, sk labaccess.SigningKey) {
	if sk.GrantMaxTTL > 0 && s.Issuer.SessionTTL() > sk.GrantMaxTTL {
		if _, warned := s.Fleet.limitWarned.LoadOrStore(member.ID.String()+"/grant", true); !warned {
			log.Warn().Str("agent", member.Name).Dur("configured", s.Issuer.SessionTTL()).Dur("proxyMax", sk.GrantMaxTTL).
				Msg("LAB_SESSION_TTL is above what the agent's proxy accepts as a grant: its grants are capped")
		}
	}
}

// The contract each feature of this build needs from an agent: a feature is pinned to the contract that brought it, never to
// the contract the SDK was built against (a newer SDK must not take a working feature away from an older agent).
const (
	// grantsContract: the grant fields and ProxyFeature.grant_max_ttl_seconds (contract 3.0).
	grantsContract = "3.0"
	// consoleLinesContract: console ports, serial ports and console cables (FeaturesResponse.console_cables, contract 3.1).
	consoleLinesContract = "3.1"
	// kernelLogContract: the kernel log on a console port (ConsolePortSpec.kernelLog, FeaturesResponse.console_cables.kernel_log,
	// contract 3.2). An older agent's API server drops the field without a word, so it is never sent to one.
	kernelLogContract = "3.2"
)

// contractAtLeast says whether an agent that reported `reported` has every call of contract `needed` (same major, minor not
// below). A report that is not readable has none.
func contractAtLeast(needed, reported string) bool {
	ok, err := labclient.Compatible(needed, reported)
	return err == nil && ok
}

// contractAccepts says whether an agent that reported `reported` takes the grants of this build (contract 3.0 or a later 3.x).
func contractAccepts(reported string) bool { return contractAtLeast(grantsContract, reported) }

// Issue signs a grant for one device of a lab group.
func (s SessionIssuer) Issue(ctx context.Context, session labaccess.Session, now time.Time) (labaccess.Link, error) {
	member, err := s.Fleet.memberOf(ctx, session.Group)
	if err != nil {
		return labaccess.Link{}, err
	}
	if member.Tenant == "" || member.AccessKeyID == "" || len(member.AccessKey) == 0 {
		return labaccess.Link{}, infraModel.ErrInfrastructureUnavailable.Err()
	}
	sk := labaccess.SigningKey{Tenant: member.Tenant, KeyID: member.AccessKeyID, Key: member.AccessKey}
	if f := member.Features.Get(); f != nil {
		if !contractAccepts(f.ContractVersion) {
			if _, warned := s.Fleet.limitWarned.LoadOrStore(member.ID.String()+"/contract", true); !warned {
				log.Warn().Str("agent", member.Name).Str("reported", f.ContractVersion).Str("needed", grantsContract).
					Msg("the agent's contract does not take the access grants of this build: no web or console link is issued")
			}
			return labaccess.Link{}, infraModel.ErrInfrastructureUnavailable.Err()
		}
		sk.GrantMaxTTL = time.Duration(f.Proxy.GrantMaxTTLSeconds) * time.Second
		s.warnLimits(member, sk)
	}
	return s.Issuer.Issue(sk, session, now)
}
