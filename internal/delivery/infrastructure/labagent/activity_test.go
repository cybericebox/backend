package labagent

import (
	"context"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	labclient "github.com/cybericebox/laboratory/pkg/agent/client"
	labpb "github.com/cybericebox/laboratory/pkg/agent/protobuf"

	infraModel "github.com/cybericebox/daemon/internal/model/infrastructure"
)

// activityAgent answers GetLabGroupActivity (and GetFeatures) from the test.
type activityAgent struct {
	labpb.LabManagerClient
	res      *labpb.GetLabGroupActivityResponse
	err      error
	got      *labpb.GetLabGroupActivityRequest
	features *labpb.FeaturesResponse
}

func (a *activityAgent) Close() error { return nil }

func (a *activityAgent) GetLabGroupActivity(_ context.Context, r *labpb.GetLabGroupActivityRequest, _ ...grpc.CallOption) (*labpb.GetLabGroupActivityResponse, error) {
	a.got = r
	return a.res, a.err
}

func (a *activityAgent) GetFeatures(context.Context, *labpb.Empty, ...grpc.CallOption) (*labpb.FeaturesResponse, error) {
	return a.features, nil
}

func activityClient(a *activityAgent) *Client {
	return &Client{Client: labclient.Client(a), instance: "prod"}
}

func TestTheActivityFeatureIsReadFromTheFeatures(t *testing.T) {
	f := FeaturesOf(&labpb.FeaturesResponse{Activity: &labpb.ActivityFeature{Enabled: true, MaxEvents: 5000, RingMinutes: 30, ConsoleMaxLifetimeS: 28800, DeviceTouch: true}})
	require.NotNil(t, f.Activity)
	assert.True(t, f.Activity.Enabled)
	assert.EqualValues(t, 30, f.Activity.RingMinutes)
	assert.EqualValues(t, 28800, f.Activity.ConsoleMaxLifetimeSeconds)
	assert.True(t, f.Activity.DeviceTouch)
	assert.False(t, FeaturesOf(&labpb.FeaturesResponse{Activity: &labpb.ActivityFeature{Enabled: true}}).Activity.DeviceTouch, "contract 2.5 agent: no touches reported")
	assert.Nil(t, FeaturesOf(&labpb.FeaturesResponse{}).Activity, "an agent older than the field reports none")
}

func TestAnActivityPageIsConvertedAndTheCursorPassedBackUnchanged(t *testing.T) {
	a := &activityAgent{res: &labpb.GetLabGroupActivityResponse{
		NextCursor: "c-2", HasMore: true, Gap: true,
		Events: []*labpb.ActivityEvent{
			{AtMs: 1, LabName: "l-1", Fact: &labpb.ActivityEvent_Egress{Egress: &labpb.InternetEgress{SourceAddress: "10.0.0.5", DstAddress: "1.1.1.1", DstPort: 443, BytesUp: 7, BytesDown: 9}}},
			{AtMs: 2, LabName: "l-1", Fact: &labpb.ActivityEvent_Console{Console: &labpb.ConsoleSession{SessionId: "s1", Device: "web", Client: "p-1", OpenedAtMs: 1000}}},
			{AtMs: 3, LabName: "l-1", Fact: &labpb.ActivityEvent_Touch{Touch: &labpb.DeviceTouch{Client: "p-1", DstAddress: "10.0.0.7", BytesOut: 4, BytesIn: 6}}},
			{AtMs: 4, LabName: "l-1"}, // a fact kind this daemon does not know
		},
	}}
	page, err := activityClient(a).GroupActivity(context.Background(), "g-1", "c-1", 500)
	require.NoError(t, err)
	assert.Equal(t, "g-1", a.got.GetLabGroupName())
	assert.Equal(t, "c-1", a.got.GetCursor(), "the cursor goes back exactly as the laboratory gave it")
	assert.EqualValues(t, 500, a.got.GetLimit())
	assert.Equal(t, "c-2", page.NextCursor)
	assert.True(t, page.HasMore)
	assert.True(t, page.Gap)
	require.Len(t, page.Events, 3)
	assert.Equal(t, "10.0.0.5", page.Events[0].Egress.SourceAddress)
	assert.Equal(t, "s1", page.Events[1].Console.SessionID)
	assert.EqualValues(t, 10, page.Events[2].Touch.BytesOut+page.Events[2].Touch.BytesIn)
}

func TestActivityRefusalsHaveTheirOwnMeaning(t *testing.T) {
	for name, tc := range map[string]struct {
		code codes.Code
		want error
	}{
		"reports off":      {codes.FailedPrecondition, infraModel.ErrActivityDisabled},
		"group is unknown": {codes.NotFound, infraModel.ErrActivityGroupGone},
		"foreign cursor":   {codes.InvalidArgument, infraModel.ErrActivityCursor},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := activityClient(&activityAgent{err: status.Error(tc.code, "x")}).GroupActivity(context.Background(), "g", "", 10)
			assert.ErrorIs(t, err, tc.want)
		})
	}
	_, err := activityClient(&activityAgent{err: status.Error(codes.Unavailable, "down")}).GroupActivity(context.Background(), "g", "", 10)
	require.Error(t, err)
	assert.NotErrorIs(t, err, infraModel.ErrActivityDisabled, "a failing agent is an error, not 'reports off'")
}

func TestTheFleetSaysWhetherTheAgentKeepsActivityReports(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		cached *infraModel.AgentFeatures
		live   *labpb.FeaturesResponse
		want   bool
	}{
		"on":            {&infraModel.AgentFeatures{Activity: &infraModel.ActivityFeature{Enabled: true}}, nil, true},
		"off":           {&infraModel.AgentFeatures{Activity: &infraModel.ActivityFeature{}}, nil, false},
		"older agent":   {&infraModel.AgentFeatures{}, nil, false},
		"not read yet":  {nil, &labpb.FeaturesResponse{Activity: &labpb.ActivityFeature{Enabled: true}}, true},
		"not read, off": {nil, &labpb.FeaturesResponse{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			a := &activityAgent{features: tc.live}
			m := &Member{ID: uuid.Must(uuid.NewV7()), Name: "env", Enabled: true, Client: activityClient(a), Features: NewFeatureCell(tc.cached)}
			got, err := NewFleet(nil, nil, m).ActivityFeature(ctx, "g")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Enabled)
		})
	}
}
