package labagent

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
)

// GroupActivity asks the agent for the raw activity facts of one lab group since a cursor (contract 2.5). The cursor is the one
// the agent gave, passed back unchanged; empty starts from the oldest event it keeps.
func (c *Client) GroupActivity(ctx context.Context, group, cursor string, limit int32) (infraModel.ActivityPage, error) {
	res, err := c.GetLabGroupActivity(ctx, &labpb.GetLabGroupActivityRequest{LabGroupName: group, Cursor: cursor, Limit: limit})
	if err != nil {
		switch status.Code(err) {
		case codes.FailedPrecondition:
			return infraModel.ActivityPage{}, infraModel.ErrActivityDisabled
		case codes.NotFound:
			return infraModel.ActivityPage{}, infraModel.ErrActivityGroupGone
		case codes.InvalidArgument:
			return infraModel.ActivityPage{}, infraModel.ErrActivityCursor
		}
		return infraModel.ActivityPage{}, agentErr("get the lab group activity", err)
	}
	return ActivityPageOf(res), nil
}

// ActivityPageOf converts the agent's answer.
func ActivityPageOf(res *labpb.GetLabGroupActivityResponse) infraModel.ActivityPage {
	page := infraModel.ActivityPage{NextCursor: res.GetNextCursor(), HasMore: res.GetHasMore(), Gap: res.GetGap()}
	for _, e := range res.GetEvents() {
		ev := infraModel.ActivityEvent{AtMs: e.GetAtMs(), Lab: e.GetLabName()}
		switch {
		case e.GetEgress() != nil:
			x := e.GetEgress()
			ev.Egress = &infraModel.InternetEgress{SourceAddress: x.GetSourceAddress(), DstAddress: x.GetDstAddress(), DstPort: x.GetDstPort(), BytesUp: x.GetBytesUp(), BytesDown: x.GetBytesDown()}
		case e.GetConsole() != nil:
			x := e.GetConsole()
			ev.Console = &infraModel.ConsoleSession{SessionID: x.GetSessionId(), Device: x.GetDevice(), Client: x.GetClient(), OpenedAtMs: x.GetOpenedAtMs(), ClosedAtMs: x.GetClosedAtMs()}
		case e.GetTouch() != nil:
			x := e.GetTouch()
			ev.Touch = &infraModel.DeviceTouch{Client: x.GetClient(), DstAddress: x.GetDstAddress(), BytesOut: x.GetBytesOut(), BytesIn: x.GetBytesIn()}
		default:
			continue // a fact kind this daemon does not know yet
		}
		page.Events = append(page.Events, ev)
	}
	return page
}

// ActivityFeature is what the agent that holds the group says of its activity reports (contract 2.5/2.6). An agent older than the
// reports, one that has not reported its features, or a group without an agent has none: the zero value (not enabled).
func (f *Fleet) ActivityFeature(ctx context.Context, group string) (infraModel.ActivityFeature, error) {
	m, err := f.memberOf(ctx, group)
	if errors.Is(err, errNoPlacement) {
		return infraModel.ActivityFeature{}, nil
	}
	if err != nil {
		return infraModel.ActivityFeature{}, err
	}
	feat := m.Features.Get()
	if feat == nil {
		res, readErr := featuresReader(m)(ctx)
		if readErr != nil {
			return infraModel.ActivityFeature{}, readErr
		}
		read := FeaturesOf(res)
		feat = &read
	}
	if feat.Activity == nil {
		return infraModel.ActivityFeature{}, nil
	}
	return *feat.Activity, nil
}

// GroupActivity is the agent's page of the group's activity. A group without an agent is gone.
func (f *Fleet) GroupActivity(ctx context.Context, group, cursor string, limit int32) (infraModel.ActivityPage, error) {
	c, err := f.route(ctx, group)
	if errors.Is(err, errNoPlacement) {
		return infraModel.ActivityPage{}, infraModel.ErrActivityGroupGone
	}
	if err != nil {
		return infraModel.ActivityPage{}, err
	}
	return c.GroupActivity(ctx, group, cursor, limit)
}
