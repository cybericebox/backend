package handler

import (
	"slices"
	"strings"
	"testing"

	"github.com/cybericebox/backend/internal/authz"
	"github.com/cybericebox/backend/internal/model/rbac"
)

// The route declarations of plan P1b: every mounted route declares its keys and scope source (authz.Decl, through
// the protector's Route) next to the legacy gate, and the declared keys map back to that gate through
// authz/legacy.go. The rules over the declarations themselves (keys in the catalogue, scope sources, .delete keys,
// no bare self in the staff areas) are tools/checkroutes, which reads the keys column of routes.golden.

// undeclaredRoutes is the ratchet of TestEveryRouteDeclaresKeys: the routes that declare nothing yet. It only
// shrinks: a route that gets its declaration must leave the list, and a new route declares from the start. It is
// empty at the end of P1b. An entry that names a reason other than its area waits for the owner: its row in
// Appendix A.3 of the access spec is missing or cannot be declared as written.
var undeclaredRoutes = map[string]string{
	// P1b area 1: auth, me, users
	"POST /api/me/draft-key": "A.3.2 #47 names any-of exercises.create, exercises.update, event.exercises.create, event.exercises.update with scope \"none\"; the event keys are not platform-grantable, so no scope source of the plan checks them (it reads like \"platform, or any event via ScopesWith\", N3): owner to settle",
	// P1b area 3: events platform side and public
	"DELETE /api/events/:id":                                      "area 3, A.3 #94",
	"GET /api/events":                                             "area 3, A.3 #92",
	"GET /api/events/:id":                                         "area 3, A.3 #95",
	"GET /api/events/:id/config":                                  "area 3, A.3 #98",
	"GET /api/events/:id/content-images/:fileID":                  "area 3, A.3 #100",
	"GET /api/events/:id/content/pages":                           "area 3, A.3 #101",
	"GET /api/events/:id/content/pages/:slug":                     "area 3, A.3 #102",
	"GET /api/events/:id/favicon/:fileID":                         "area 3, A.3 #103",
	"GET /api/events/:id/logo/:fileID":                            "area 3, A.3 #105",
	"GET /api/events/:id/participants":                            "area 3, A.3 #106",
	"GET /api/events/:id/preview-picture/:fileID":                 "area 3, A.3 #109",
	"GET /api/events/:id/results":                                 "area 3, A.3 #110",
	"GET /api/events/:id/results/changes":                         "area 3, A.3 #111",
	"GET /api/events/:id/results/live":                            "area 3, A.3 #112",
	"GET /api/events/:id/solution-attempts":                       "area 3, A.3 #113",
	"GET /api/events/facets":                                      "area 3, A.3 #117",
	"GET /api/events/organizers":                                  "area 3, A.3 #118",
	"GET /api/render/events/:tag/manifest":                        "area 3, A.3 #119",
	"GET /api/render/events/:tag/page":                            "area 3, A.3 #120",
	"GET /api/render/key":                                         "area 3, A.3 #121",
	"PATCH /api/events/:id/solution-attempts/:attemptID/decision": "area 3, A.3 #114",
	"POST /api/events":                                            "area 3, A.3 #93",
	"POST /api/events/:id/archive":                                "area 3, A.3 #97",
	"POST /api/events/:id/participants/:userID/approve":           "area 3, A.3 #107",
	"POST /api/events/:id/participants/:userID/reject":            "area 3, A.3 #108",
	"POST /api/events/bulk/archive":                               "area 3, A.3 #116",
	"PUT /api/events/:id":                                         "area 3, A.3 #96",
	"PUT /api/events/:id/config":                                  "area 3, A.3 #99",
	"PUT /api/events/:id/infrastructure":                          "area 3, A.3 #104",
	"PUT /api/events/:id/theme":                                   "area 3, A.3 #115",
	// P1b area 4: /events/:id/manage
	"DELETE /api/events/:id/manage/analytics/integrity/dismissals/:dismissalID":                                  "area 4, A.3 #129",
	"DELETE /api/events/:id/manage/analytics/integrity/solves/:teamChallengeID/review":                           "area 4, A.3 #132",
	"DELETE /api/events/:id/manage/banners/:bannerID":                                                            "area 4, A.3 #163",
	"DELETE /api/events/:id/manage/challenge-groups/:groupID":                                                    "area 4, A.3 #175",
	"DELETE /api/events/:id/manage/content/landing/draft":                                                        "area 4, A.3 #184",
	"DELETE /api/events/:id/manage/content/live/screen-link":                                                     "area 4, A.3 #190",
	"DELETE /api/events/:id/manage/content/live/screens/:screenID":                                               "area 4, A.3 #197",
	"DELETE /api/events/:id/manage/content/live/screens/:screenID/link":                                          "area 4, A.3 #202",
	"DELETE /api/events/:id/manage/exercises/:exerciseID":                                                        "area 4, A.3 #215",
	"DELETE /api/events/:id/manage/logo":                                                                         "area 4, A.3 #265",
	"DELETE /api/events/:id/manage/mail/smtp":                                                                    "area 4, A.3 #270",
	"DELETE /api/events/:id/manage/notification-subscriptions/:signalType/:channel":                              "area 4, A.3 #277",
	"DELETE /api/events/:id/manage/notification-templates/custom/:templateID":                                    "area 4, A.3 #280",
	"DELETE /api/events/:id/manage/notification-templates/email/:templateID":                                     "area 4, A.3 #285",
	"DELETE /api/events/:id/manage/notification-templates/email/type/:notificationType":                          "area 4, A.3 #297",
	"DELETE /api/events/:id/manage/notification-templates/in-app/:templateID":                                    "area 4, A.3 #300",
	"DELETE /api/events/:id/manage/notification-templates/in-app/type/:notificationType":                         "area 4, A.3 #306",
	"DELETE /api/events/:id/manage/pages/:pageID":                                                                "area 4, A.3 #310",
	"DELETE /api/events/:id/manage/pages/:pageID/draft":                                                          "area 4, A.3 #312",
	"DELETE /api/events/:id/manage/participant-preview/labs/:deployID":                                           "area 4, A.3 #320",
	"DELETE /api/events/:id/manage/participant-preview/runs":                                                     "area 4, A.3 #321",
	"DELETE /api/events/:id/manage/participant-preview/selections/:selectionID/run":                              "area 4, A.3 #324",
	"DELETE /api/events/:id/manage/participants/:userID/invitation":                                              "area 4, A.3 #336",
	"DELETE /api/events/:id/manage/preview-picture":                                                              "area 4, A.3 #346",
	"DELETE /api/events/:id/manage/registry-secrets/:secretId":                                                   "area 4, A.3 #351",
	"DELETE /api/events/:id/manage/stages/:stageID":                                                              "area 4, A.3 #371",
	"DELETE /api/events/:id/manage/teams/:teamID":                                                                "area 4, A.3 #377",
	"DELETE /api/events/:id/manage/teams/:teamID/members/:userID":                                                "area 4, A.3 #385",
	"DELETE /api/events/:id/managers/:userID":                                                                    "area 4, A.3 #393",
	"GET /api/events/:id/manage/access":                                                                          "area 4, A.3 #122",
	"GET /api/events/:id/manage/analytics/access":                                                                "area 4, A.3 #123",
	"GET /api/events/:id/manage/analytics/communications":                                                        "area 4, A.3 #124",
	"GET /api/events/:id/manage/analytics/communications/export.csv":                                             "area 4, A.3 #125",
	"GET /api/events/:id/manage/analytics/integrity":                                                             "area 4, A.3 #126",
	"GET /api/events/:id/manage/analytics/integrity/dismissals":                                                  "area 4, A.3 #127",
	"GET /api/events/:id/manage/analytics/integrity/export.csv":                                                  "area 4, A.3 #130",
	"GET /api/events/:id/manage/analytics/integrity/flags":                                                       "area 4, A.3 #131",
	"GET /api/events/:id/manage/analytics/lab-telemetry":                                                         "area 4, A.3 #134",
	"GET /api/events/:id/manage/analytics/lab-telemetry/transitions":                                             "area 4, A.3 #135",
	"GET /api/events/:id/manage/analytics/overview":                                                              "area 4, A.3 #136",
	"GET /api/events/:id/manage/analytics/overview/export.csv":                                                   "area 4, A.3 #137",
	"GET /api/events/:id/manage/analytics/participants":                                                          "area 4, A.3 #138",
	"GET /api/events/:id/manage/analytics/participants/export.csv":                                               "area 4, A.3 #139",
	"GET /api/events/:id/manage/analytics/progress/heatmap":                                                      "area 4, A.3 #140",
	"GET /api/events/:id/manage/analytics/progress/inactive":                                                     "area 4, A.3 #141",
	"GET /api/events/:id/manage/analytics/progress/inactive/export.csv":                                          "area 4, A.3 #142",
	"GET /api/events/:id/manage/analytics/progress/matrix":                                                       "area 4, A.3 #143",
	"GET /api/events/:id/manage/analytics/progress/matrix/export.csv":                                            "area 4, A.3 #144",
	"GET /api/events/:id/manage/analytics/progress/scores":                                                       "area 4, A.3 #145",
	"GET /api/events/:id/manage/analytics/report":                                                                "area 4, A.3 #146",
	"GET /api/events/:id/manage/analytics/report/export.zip":                                                     "area 4, A.3 #147",
	"GET /api/events/:id/manage/analytics/stands":                                                                "area 4, A.3 #148",
	"GET /api/events/:id/manage/analytics/stands/export.csv":                                                     "area 4, A.3 #149",
	"GET /api/events/:id/manage/analytics/submissions":                                                           "area 4, A.3 #150",
	"GET /api/events/:id/manage/analytics/submissions/export.csv":                                                "area 4, A.3 #151",
	"GET /api/events/:id/manage/analytics/tasks":                                                                 "area 4, A.3 #152",
	"GET /api/events/:id/manage/analytics/tasks/:challengeID":                                                    "area 4, A.3 #153",
	"GET /api/events/:id/manage/analytics/tasks/:challengeID/wrong-answers":                                      "area 4, A.3 #154",
	"GET /api/events/:id/manage/analytics/tasks/export.csv":                                                      "area 4, A.3 #155",
	"GET /api/events/:id/manage/analytics/usage":                                                                 "area 4, A.3 #156",
	"GET /api/events/:id/manage/analytics/usage/export.csv":                                                      "area 4, A.3 #157",
	"GET /api/events/:id/manage/answer-files/:fileID":                                                            "area 4, A.3 #159",
	"GET /api/events/:id/manage/banners":                                                                         "area 4, A.3 #161",
	"GET /api/events/:id/manage/broadcasts":                                                                      "area 4, A.3 #166",
	"GET /api/events/:id/manage/broadcasts/:broadcastID":                                                         "area 4, A.3 #168",
	"GET /api/events/:id/manage/broadcasts/:broadcastID/deliveries":                                              "area 4, A.3 #170",
	"GET /api/events/:id/manage/broadcasts/:broadcastID/deliveries/export":                                       "area 4, A.3 #171",
	"GET /api/events/:id/manage/challenge-groups":                                                                "area 4, A.3 #173",
	"GET /api/events/:id/manage/config":                                                                          "area 4, A.3 #179",
	"GET /api/events/:id/manage/content":                                                                         "area 4, A.3 #181",
	"GET /api/events/:id/manage/content/live":                                                                    "area 4, A.3 #186",
	"GET /api/events/:id/manage/content/live/screen-link":                                                        "area 4, A.3 #191",
	"GET /api/events/:id/manage/content/live/screens":                                                            "area 4, A.3 #195",
	"GET /api/events/:id/manage/content/live/screens/:screenID":                                                  "area 4, A.3 #198",
	"GET /api/events/:id/manage/content/live/screens/:screenID/link":                                             "area 4, A.3 #203",
	"GET /api/events/:id/manage/content/live/screens/:screenID/version":                                          "area 4, A.3 #207",
	"GET /api/events/:id/manage/content/live/version":                                                            "area 4, A.3 #208",
	"GET /api/events/:id/manage/content/variables":                                                               "area 4, A.3 #209",
	"GET /api/events/:id/manage/exercise-catalog":                                                                "area 4, A.3 #210",
	"GET /api/events/:id/manage/exercise-catalog/:versionID":                                                     "area 4, A.3 #211",
	"GET /api/events/:id/manage/exercise-catalog/tags":                                                           "area 4, A.3 #212",
	"GET /api/events/:id/manage/exercises":                                                                       "area 4, A.3 #213",
	"GET /api/events/:id/manage/exercises/:exerciseID/challenges":                                                "area 4, A.3 #216",
	"GET /api/events/:id/manage/exercises/:exerciseID/team-copies":                                               "area 4, A.3 #226",
	"GET /api/events/:id/manage/forms":                                                                           "area 4, A.3 #234",
	"GET /api/events/:id/manage/forms/:formID":                                                                   "area 4, A.3 #236",
	"GET /api/events/:id/manage/forms/:formID/answers":                                                           "area 4, A.3 #238",
	"GET /api/events/:id/manage/forms/:formID/deliveries":                                                        "area 4, A.3 #239",
	"GET /api/events/:id/manage/hint-unlocks":                                                                    "area 4, A.3 #242",
	"GET /api/events/:id/manage/images/availability":                                                             "area 4, A.3 #243",
	"GET /api/events/:id/manage/labs":                                                                            "area 4, A.3 #244",
	"GET /api/events/:id/manage/labs/:teamID/detail":                                                             "area 4, A.3 #247",
	"GET /api/events/:id/manage/labs/moderators/board":                                                           "area 4, A.3 #249",
	"GET /api/events/:id/manage/labs/moderators/challenges":                                                      "area 4, A.3 #250",
	"GET /api/events/:id/manage/labs/moderators/challenges/:challengeID/files/:fileID":                           "area 4, A.3 #251",
	"GET /api/events/:id/manage/labs/moderators/challenges/:challengeID/lab":                                     "area 4, A.3 #252",
	"GET /api/events/:id/manage/labs/moderators/challenges/:challengeID/solves":                                  "area 4, A.3 #254",
	"GET /api/events/:id/manage/labs/moderators/team":                                                            "area 4, A.3 #256",
	"GET /api/events/:id/manage/labs/moderators/vpn":                                                             "area 4, A.3 #257",
	"GET /api/events/:id/manage/labs/moderators/vpn/status":                                                      "area 4, A.3 #258",
	"GET /api/events/:id/manage/lifecycle":                                                                       "area 4, A.3 #260",
	"GET /api/events/:id/manage/lifecycle/changes":                                                               "area 4, A.3 #262",
	"GET /api/events/:id/manage/list-columns/:list":                                                              "area 4, A.3 #263",
	"GET /api/events/:id/manage/mail":                                                                            "area 4, A.3 #267",
	"GET /api/events/:id/manage/mail/journal":                                                                    "area 4, A.3 #269",
	"GET /api/events/:id/manage/name":                                                                            "area 4, A.3 #273",
	"GET /api/events/:id/manage/notification-subscriptions":                                                      "area 4, A.3 #275",
	"GET /api/events/:id/manage/notification-templates/custom":                                                   "area 4, A.3 #278",
	"GET /api/events/:id/manage/notification-templates/custom/:templateID":                                       "area 4, A.3 #281",
	"GET /api/events/:id/manage/notification-templates/email":                                                    "area 4, A.3 #283",
	"GET /api/events/:id/manage/notification-templates/email/:templateID":                                        "area 4, A.3 #286",
	"GET /api/events/:id/manage/notification-templates/email/brand/logo":                                         "area 4, A.3 #293",
	"GET /api/events/:id/manage/notification-templates/email/images/:fileID":                                     "area 4, A.3 #294",
	"GET /api/events/:id/manage/notification-templates/email/presets":                                            "area 4, A.3 #295",
	"GET /api/events/:id/manage/notification-templates/in-app":                                                   "area 4, A.3 #298",
	"GET /api/events/:id/manage/notification-templates/in-app/:templateID":                                       "area 4, A.3 #301",
	"GET /api/events/:id/manage/notification-types":                                                              "area 4, A.3 #307",
	"GET /api/events/:id/manage/pages":                                                                           "area 4, A.3 #308",
	"GET /api/events/:id/manage/pages/:slug":                                                                     "area 4, A.3 #314",
	"GET /api/events/:id/manage/participant-form":                                                                "area 4, A.3 #315",
	"GET /api/events/:id/manage/participant-form/answers":                                                        "area 4, A.3 #317",
	"GET /api/events/:id/manage/participant-preview/board":                                                       "area 4, A.3 #318",
	"GET /api/events/:id/manage/participant-preview/catalog":                                                     "area 4, A.3 #319",
	"GET /api/events/:id/manage/participant-preview/selections/:selectionID/lab":                                 "area 4, A.3 #322",
	"GET /api/events/:id/manage/participant-preview/selections/:selectionID/tasks/:taskID/files/:fileID":         "area 4, A.3 #326",
	"GET /api/events/:id/manage/participant-preview/selections/:selectionID/tasks/:taskID/solves":                "area 4, A.3 #328",
	"GET /api/events/:id/manage/participant-preview/selections/:selectionID/vpn":                                 "area 4, A.3 #330",
	"GET /api/events/:id/manage/participant-preview/settings":                                                    "area 4, A.3 #331",
	"GET /api/events/:id/manage/participants":                                                                    "area 4, A.3 #333",
	"GET /api/events/:id/manage/participants/:userID":                                                            "area 4, A.3 #334",
	"GET /api/events/:id/manage/participants/:userID/staff-fields":                                               "area 4, A.3 #341",
	"GET /api/events/:id/manage/readiness":                                                                       "area 4, A.3 #348",
	"GET /api/events/:id/manage/registry-secrets":                                                                "area 4, A.3 #349",
	"GET /api/events/:id/manage/resource-plan":                                                                   "area 4, A.3 #354",
	"GET /api/events/:id/manage/resources":                                                                       "area 4, A.3 #355",
	"GET /api/events/:id/manage/results":                                                                         "area 4, A.3 #357",
	"GET /api/events/:id/manage/results-settings":                                                                "area 4, A.3 #358",
	"GET /api/events/:id/manage/results/export.csv":                                                              "area 4, A.3 #360",
	"GET /api/events/:id/manage/scoring":                                                                         "area 4, A.3 #362",
	"GET /api/events/:id/manage/solution-attempts":                                                               "area 4, A.3 #364",
	"GET /api/events/:id/manage/solution-attempts/export.csv":                                                    "area 4, A.3 #367",
	"GET /api/events/:id/manage/solution-attempts/live":                                                          "area 4, A.3 #368",
	"GET /api/events/:id/manage/stages":                                                                          "area 4, A.3 #369",
	"GET /api/events/:id/manage/team-fields":                                                                     "area 4, A.3 #373",
	"GET /api/events/:id/manage/teams":                                                                           "area 4, A.3 #375",
	"GET /api/events/:id/manage/teams/:teamID":                                                                   "area 4, A.3 #378",
	"GET /api/events/:id/manage/teams/:teamID/staff-fields":                                                      "area 4, A.3 #387",
	"GET /api/events/:id/managers":                                                                               "area 4, A.3 #392",
	"PATCH /api/events/:id/manage/content/live/screens/:screenID":                                                "area 4, A.3 #199",
	"PATCH /api/events/:id/manage/solution-attempts/:attemptID/decision":                                         "area 4, A.3 #365",
	"POST /api/events/:id/manage/analytics/integrity/dismissals":                                                 "area 4, A.3 #128",
	"POST /api/events/:id/manage/answer-files":                                                                   "area 4, A.3 #158",
	"POST /api/events/:id/manage/banners":                                                                        "area 4, A.3 #162",
	"POST /api/events/:id/manage/brand-drafts/:kind":                                                             "area 4, A.3 #165",
	"POST /api/events/:id/manage/broadcasts":                                                                     "area 4, A.3 #167",
	"POST /api/events/:id/manage/broadcasts/:broadcastID/cancel":                                                 "area 4, A.3 #169",
	"POST /api/events/:id/manage/broadcasts/audience-count":                                                      "area 4, A.3 #172",
	"POST /api/events/:id/manage/challenge-groups":                                                               "area 4, A.3 #174",
	"POST /api/events/:id/manage/content-images":                                                                 "area 4, A.3 #182",
	"POST /api/events/:id/manage/content/landing/publish":                                                        "area 4, A.3 #185",
	"POST /api/events/:id/manage/content/live/logos":                                                             "area 4, A.3 #188",
	"POST /api/events/:id/manage/content/live/publish":                                                           "area 4, A.3 #189",
	"POST /api/events/:id/manage/content/live/screen-link":                                                       "area 4, A.3 #192",
	"POST /api/events/:id/manage/content/live/screen-link/regenerate":                                            "area 4, A.3 #193",
	"POST /api/events/:id/manage/content/live/screens":                                                           "area 4, A.3 #196",
	"POST /api/events/:id/manage/content/live/screens/:screenID/duplicate":                                       "area 4, A.3 #201",
	"POST /api/events/:id/manage/content/live/screens/:screenID/link":                                            "area 4, A.3 #204",
	"POST /api/events/:id/manage/content/live/screens/:screenID/link/regenerate":                                 "area 4, A.3 #205",
	"POST /api/events/:id/manage/content/live/screens/:screenID/publish":                                         "area 4, A.3 #206",
	"POST /api/events/:id/manage/exercises":                                                                      "area 4, A.3 #214",
	"POST /api/events/:id/manage/exercises/:exerciseID/fork":                                                     "area 4, A.3 #222",
	"POST /api/events/:id/manage/exercises/:exerciseID/replace":                                                  "area 4, A.3 #223",
	"POST /api/events/:id/manage/exercises/:exerciseID/revert":                                                   "area 4, A.3 #224",
	"POST /api/events/:id/manage/exercises/:exerciseID/team-copies/sync":                                         "area 4, A.3 #227",
	"POST /api/events/:id/manage/exercises/:exerciseID/update":                                                   "area 4, A.3 #228",
	"POST /api/events/:id/manage/exercises/:exerciseID/update/preview":                                           "area 4, A.3 #229",
	"POST /api/events/:id/manage/exercises/bulk/detach":                                                          "area 4, A.3 #231",
	"POST /api/events/:id/manage/forms":                                                                          "area 4, A.3 #235",
	"POST /api/events/:id/manage/forms/:formID/deliveries":                                                       "area 4, A.3 #240",
	"POST /api/events/:id/manage/labs/:teamID/challenges/:challengeID/devices/:device/rescue":                    "area 4, A.3 #245",
	"POST /api/events/:id/manage/labs/:teamID/challenges/:challengeID/devices/:device/reset":                     "area 4, A.3 #246",
	"POST /api/events/:id/manage/labs/:teamID/recreate":                                                          "area 4, A.3 #248",
	"POST /api/events/:id/manage/labs/moderators/challenges/:challengeID/lab/link":                               "area 4, A.3 #253",
	"POST /api/events/:id/manage/labs/moderators/challenges/:challengeID/submit":                                 "area 4, A.3 #255",
	"POST /api/events/:id/manage/logo":                                                                           "area 4, A.3 #266",
	"POST /api/events/:id/manage/mail/smtp/test":                                                                 "area 4, A.3 #272",
	"POST /api/events/:id/manage/notification-templates/custom":                                                  "area 4, A.3 #279",
	"POST /api/events/:id/manage/notification-templates/email":                                                   "area 4, A.3 #284",
	"POST /api/events/:id/manage/notification-templates/email/:templateID/customize":                             "area 4, A.3 #288",
	"POST /api/events/:id/manage/notification-templates/email/:templateID/images":                                "area 4, A.3 #289",
	"POST /api/events/:id/manage/notification-templates/email/:templateID/publish":                               "area 4, A.3 #290",
	"POST /api/events/:id/manage/notification-templates/email/:templateID/rollback":                              "area 4, A.3 #291",
	"POST /api/events/:id/manage/notification-templates/email/:templateID/test":                                  "area 4, A.3 #292",
	"POST /api/events/:id/manage/notification-templates/email/preview":                                           "area 4, A.3 #296",
	"POST /api/events/:id/manage/notification-templates/in-app":                                                  "area 4, A.3 #299",
	"POST /api/events/:id/manage/notification-templates/in-app/:templateID/customize":                            "area 4, A.3 #303",
	"POST /api/events/:id/manage/notification-templates/in-app/:templateID/publish":                              "area 4, A.3 #304",
	"POST /api/events/:id/manage/notification-templates/in-app/:templateID/rollback":                             "area 4, A.3 #305",
	"POST /api/events/:id/manage/pages":                                                                          "area 4, A.3 #309",
	"POST /api/events/:id/manage/pages/:pageID/publish":                                                          "area 4, A.3 #313",
	"POST /api/events/:id/manage/participant-preview/selections/:selectionID/proxy":                              "area 4, A.3 #323",
	"POST /api/events/:id/manage/participant-preview/selections/:selectionID/start":                              "area 4, A.3 #325",
	"POST /api/events/:id/manage/participant-preview/selections/:selectionID/tasks/:taskID/hints/:hintID/unlock": "area 4, A.3 #327",
	"POST /api/events/:id/manage/participant-preview/selections/:selectionID/tasks/:taskID/submit":               "area 4, A.3 #329",
	"POST /api/events/:id/manage/participants/:userID/approve":                                                   "area 4, A.3 #335",
	"POST /api/events/:id/manage/participants/:userID/invitation/accept":                                         "area 4, A.3 #337",
	"POST /api/events/:id/manage/participants/:userID/invitation/resend":                                         "area 4, A.3 #339",
	"POST /api/events/:id/manage/participants/:userID/reject":                                                    "area 4, A.3 #340",
	"POST /api/events/:id/manage/participants/invitations":                                                       "area 4, A.3 #345",
	"POST /api/events/:id/manage/preview-picture":                                                                "area 4, A.3 #347",
	"POST /api/events/:id/manage/registry-secrets":                                                               "area 4, A.3 #350",
	"POST /api/events/:id/manage/registry-secrets/:secretId/check":                                               "area 4, A.3 #353",
	"POST /api/events/:id/manage/resources/change-requests":                                                      "area 4, A.3 #356",
	"POST /api/events/:id/manage/solution-attempts/annul":                                                        "area 4, A.3 #366",
	"POST /api/events/:id/manage/stages":                                                                         "area 4, A.3 #370",
	"POST /api/events/:id/manage/teams":                                                                          "area 4, A.3 #376",
	"POST /api/events/:id/manage/teams/:teamID/form":                                                             "area 4, A.3 #382",
	"POST /api/events/:id/manage/teams/:teamID/invitations":                                                      "area 4, A.3 #384",
	"POST /api/events/:id/manage/teams/:teamID/members/:userID":                                                  "area 4, A.3 #386",
	"POST /api/events/:id/manage/teams/batch":                                                                    "area 4, A.3 #389",
	"POST /api/events/:id/manage/teams/form":                                                                     "area 4, A.3 #390",
	"PUT /api/events/:id/manage/analytics/integrity/solves/:teamChallengeID/review":                              "area 4, A.3 #133",
	"PUT /api/events/:id/manage/appearance":                                                                      "area 4, A.3 #160",
	"PUT /api/events/:id/manage/banners/:bannerID":                                                               "area 4, A.3 #164",
	"PUT /api/events/:id/manage/challenge-groups/:groupID":                                                       "area 4, A.3 #176",
	"PUT /api/events/:id/manage/challenge-groups/order":                                                          "area 4, A.3 #177",
	"PUT /api/events/:id/manage/challenge-order":                                                                 "area 4, A.3 #178",
	"PUT /api/events/:id/manage/config":                                                                          "area 4, A.3 #180",
	"PUT /api/events/:id/manage/content/landing":                                                                 "area 4, A.3 #183",
	"PUT /api/events/:id/manage/content/live":                                                                    "area 4, A.3 #187",
	"PUT /api/events/:id/manage/content/live/screen-order":                                                       "area 4, A.3 #194",
	"PUT /api/events/:id/manage/content/live/screens/:screenID/draft":                                            "area 4, A.3 #200",
	"PUT /api/events/:id/manage/exercises/:exerciseID/challenges/:challengeID":                                   "area 4, A.3 #217",
	"PUT /api/events/:id/manage/exercises/:exerciseID/challenges/:challengeID/hints":                             "area 4, A.3 #218",
	"PUT /api/events/:id/manage/exercises/:exerciseID/challenges/:challengeID/relations":                         "area 4, A.3 #219",
	"PUT /api/events/:id/manage/exercises/:exerciseID/challenges/order":                                          "area 4, A.3 #220",
	"PUT /api/events/:id/manage/exercises/:exerciseID/challenges/scoring":                                        "area 4, A.3 #221",
	"PUT /api/events/:id/manage/exercises/:exerciseID/stage":                                                     "area 4, A.3 #225",
	"PUT /api/events/:id/manage/exercises/:exerciseID/visibility":                                                "area 4, A.3 #230",
	"PUT /api/events/:id/manage/exercises/bulk/scoring":                                                          "area 4, A.3 #232",
	"PUT /api/events/:id/manage/exercises/bulk/visibility":                                                       "area 4, A.3 #233",
	"PUT /api/events/:id/manage/forms/:formID":                                                                   "area 4, A.3 #237",
	"PUT /api/events/:id/manage/general":                                                                         "area 4, A.3 #241",
	"PUT /api/events/:id/manage/labs/settings":                                                                   "area 4, A.3 #259",
	"PUT /api/events/:id/manage/lifecycle":                                                                       "area 4, A.3 #261",
	"PUT /api/events/:id/manage/list-columns/:list":                                                              "area 4, A.3 #264",
	"PUT /api/events/:id/manage/mail/identity":                                                                   "area 4, A.3 #268",
	"PUT /api/events/:id/manage/mail/smtp":                                                                       "area 4, A.3 #271",
	"PUT /api/events/:id/manage/name":                                                                            "area 4, A.3 #274",
	"PUT /api/events/:id/manage/notification-subscriptions":                                                      "area 4, A.3 #276",
	"PUT /api/events/:id/manage/notification-templates/custom/:templateID":                                       "area 4, A.3 #282",
	"PUT /api/events/:id/manage/notification-templates/email/:templateID":                                        "area 4, A.3 #287",
	"PUT /api/events/:id/manage/notification-templates/in-app/:templateID":                                       "area 4, A.3 #302",
	"PUT /api/events/:id/manage/pages/:pageID":                                                                   "area 4, A.3 #311",
	"PUT /api/events/:id/manage/participant-form":                                                                "area 4, A.3 #316",
	"PUT /api/events/:id/manage/participant-preview/settings":                                                    "area 4, A.3 #332",
	"PUT /api/events/:id/manage/participants/:userID/invitation/email":                                           "area 4, A.3 #338",
	"PUT /api/events/:id/manage/participants/:userID/staff-fields":                                               "area 4, A.3 #342",
	"PUT /api/events/:id/manage/participants/:userID/team/:teamID":                                               "area 4, A.3 #343",
	"PUT /api/events/:id/manage/participants/:userID/visibility":                                                 "area 4, A.3 #344",
	"PUT /api/events/:id/manage/registry-secrets/:secretId":                                                      "area 4, A.3 #352",
	"PUT /api/events/:id/manage/results-settings":                                                                "area 4, A.3 #359",
	"PUT /api/events/:id/manage/results/opened":                                                                  "area 4, A.3 #361",
	"PUT /api/events/:id/manage/scoring":                                                                         "area 4, A.3 #363",
	"PUT /api/events/:id/manage/stages/:stageID":                                                                 "area 4, A.3 #372",
	"PUT /api/events/:id/manage/team-fields":                                                                     "area 4, A.3 #374",
	"PUT /api/events/:id/manage/teams/:teamID":                                                                   "area 4, A.3 #379",
	"PUT /api/events/:id/manage/teams/:teamID/admission":                                                         "area 4, A.3 #380",
	"PUT /api/events/:id/manage/teams/:teamID/captain/:userID":                                                   "area 4, A.3 #381",
	"PUT /api/events/:id/manage/teams/:teamID/hidden":                                                            "area 4, A.3 #383",
	"PUT /api/events/:id/manage/teams/:teamID/staff-fields":                                                      "area 4, A.3 #388",
	"PUT /api/events/:id/manage/theme":                                                                           "area 4, A.3 #391",
	"PUT /api/events/:id/managers/:userID":                                                                       "area 4, A.3 #394",
	// P1b area 5: participant routes
	"DELETE /api/events/:id/teams/:teamID":                                    "area 5, A.3 #401",
	"GET /api/events/:id/forms/:formID":                                       "area 5, A.3 #395",
	"GET /api/events/:id/forms/pending":                                       "area 5, A.3 #397",
	"GET /api/events/:id/results/participation":                               "area 5, A.3 #398",
	"GET /api/events/:id/results/team":                                        "area 5, A.3 #399",
	"GET /api/events/:id/teams/challenges/:challengeID/files/:fileID":         "area 5, A.3 #407",
	"GET /api/events/:id/teams/challenges/:challengeID/lab":                   "area 5, A.3 #409",
	"GET /api/events/:id/teams/challenges/:challengeID/solves":                "area 5, A.3 #413",
	"GET /api/events/:id/teams/challenges/mine":                               "area 5, A.3 #415",
	"GET /api/events/:id/teams/labs/:labID":                                   "area 5, A.3 #417",
	"GET /api/events/:id/teams/labs/stand":                                    "area 5, A.3 #420",
	"GET /api/events/:id/teams/labs/vpn":                                      "area 5, A.3 #421",
	"GET /api/events/:id/teams/labs/vpn/status":                               "area 5, A.3 #422",
	"GET /api/events/:id/teams/mine":                                          "area 5, A.3 #423",
	"GET /api/events/:id/teams/mine/members":                                  "area 5, A.3 #426",
	"GET /api/events/self/answer-files/:fileID":                               "area 5, A.3 #428",
	"GET /api/events/self/info":                                               "area 5, A.3 #429",
	"GET /api/events/self/join/info":                                          "area 5, A.3 #433",
	"GET /api/events/self/live-screen":                                        "area 5, A.3 #435",
	"GET /api/events/self/live-screen/results":                                "area 5, A.3 #436",
	"GET /api/events/self/live-screen/results/live":                           "area 5, A.3 #437",
	"GET /api/events/self/live-screen/version":                                "area 5, A.3 #438",
	"GET /api/events/self/participant-answers":                                "area 5, A.3 #439",
	"GET /api/events/self/participant-form":                                   "area 5, A.3 #441",
	"GET /api/events/self/participant-info":                                   "area 5, A.3 #443",
	"GET /api/events/self/public-info":                                        "area 5, A.3 #445",
	"GET /api/events/self/team-fields":                                        "area 5, A.3 #447",
	"POST /api/events/:id/forms/:formID/response":                             "area 5, A.3 #396",
	"POST /api/events/:id/teams":                                              "area 5, A.3 #400",
	"POST /api/events/:id/teams/:teamID/captain":                              "area 5, A.3 #403",
	"POST /api/events/:id/teams/:teamID/join-code":                            "area 5, A.3 #405",
	"POST /api/events/:id/teams/:teamID/members/:userID/kick":                 "area 5, A.3 #406",
	"POST /api/events/:id/teams/challenges/:challengeID/hints/:hintID/unlock": "area 5, A.3 #408",
	"POST /api/events/:id/teams/challenges/:challengeID/lab/link":             "area 5, A.3 #410",
	"POST /api/events/:id/teams/challenges/:challengeID/open":                 "area 5, A.3 #411",
	"POST /api/events/:id/teams/challenges/:challengeID/run-checks":           "area 5, A.3 #412",
	"POST /api/events/:id/teams/challenges/:challengeID/submit":               "area 5, A.3 #414",
	"POST /api/events/:id/teams/join":                                         "area 5, A.3 #416",
	"POST /api/events/:id/teams/labs/:labID/restart":                          "area 5, A.3 #418",
	"POST /api/events/:id/teams/labs/:labID/stop":                             "area 5, A.3 #419",
	"POST /api/events/:id/teams/mine/form":                                    "area 5, A.3 #424",
	"POST /api/events/:id/teams/mine/leave":                                   "area 5, A.3 #425",
	"POST /api/events/self/answer-files":                                      "area 5, A.3 #427",
	"POST /api/events/self/invitation/accept":                                 "area 5, A.3 #430",
	"POST /api/events/self/invitation/decline":                                "area 5, A.3 #431",
	"POST /api/events/self/join":                                              "area 5, A.3 #432",
	"POST /api/events/self/join/invitation/accept":                            "area 5, A.3 #434",
	"POST /api/events/self/participant-form":                                  "area 5, A.3 #442",
	"POST /api/events/self/render-grant":                                      "area 5, A.3 #446",
	"PUT /api/events/:id/teams/:teamID":                                       "area 5, A.3 #402",
	"PUT /api/events/:id/teams/:teamID/fields":                                "area 5, A.3 #404",
	"PUT /api/events/self/participant-answers":                                "area 5, A.3 #440",
	"PUT /api/events/self/pseudonym":                                          "area 5, A.3 #444",
	// P1b area 6: exercises, runs, assistant
	"DELETE /api/assistant/sessions/:id":                                                                "area 6, A.3 #83",
	"DELETE /api/assistant/sessions/:id/attachments/:aid":                                               "area 6, A.3 #86",
	"DELETE /api/exercises/:id":                                                                         "area 6, A.3 #450",
	"DELETE /api/exercises/:id/draft":                                                                   "area 6, A.3 #456",
	"DELETE /api/exercises/deploys/:deployID":                                                           "area 6, A.3 #497",
	"DELETE /api/exercises/import/jobs/:jobID":                                                          "area 6, A.3 #522",
	"DELETE /api/exercises/test-labs/bookings/:bookingID":                                               "area 6, A.3 #536",
	"DELETE /api/exercises/uploads/:uploadID":                                                           "area 6, A.3 #540",
	"GET /api/assistant/ideas":                                                                          "area 6, A.3 #81",
	"GET /api/assistant/sessions/:id/archive":                                                           "area 6, A.3 #84",
	"GET /api/assistant/sessions/:id/events":                                                            "area 6, A.3 #87",
	"GET /api/exercises":                                                                                "area 6, A.3 #448",
	"GET /api/exercises/:id":                                                                            "area 6, A.3 #451",
	"GET /api/exercises/:id/draft":                                                                      "area 6, A.3 #457",
	"GET /api/exercises/:id/draft/document":                                                             "area 6, A.3 #459",
	"GET /api/exercises/:id/elevation":                                                                  "area 6, A.3 #460",
	"GET /api/exercises/:id/images/blobs/:digest":                                                       "area 6, A.3 #465",
	"GET /api/exercises/:id/usage":                                                                      "area 6, A.3 #472",
	"GET /api/exercises/:id/variant-stats":                                                              "area 6, A.3 #473",
	"GET /api/exercises/:id/versions":                                                                   "area 6, A.3 #474",
	"GET /api/exercises/:id/versions/:versionID":                                                        "area 6, A.3 #475",
	"GET /api/exercises/:id/versions/:versionID/variants/:variantID/measure":                            "area 6, A.3 #479",
	"GET /api/exercises/:id/versions/:versionID/variants/:variantID/measure/:runID":                     "area 6, A.3 #481",
	"GET /api/exercises/:id/versions/:versionID/variants/:variantID/measure/:runID/events":              "area 6, A.3 #482",
	"GET /api/exercises/:id/versions/:versionID/variants/:variantID/opponents/:index/try":               "area 6, A.3 #484",
	"GET /api/exercises/:id/versions/:versionID/variants/:variantID/opponents/:index/try/:runID":        "area 6, A.3 #486",
	"GET /api/exercises/:id/versions/:versionID/variants/:variantID/opponents/:index/try/:runID/events": "area 6, A.3 #487",
	"GET /api/exercises/access":                                                                         "area 6, A.3 #490",
	"GET /api/exercises/authors":                                                                        "area 6, A.3 #491",
	"GET /api/exercises/capabilities":                                                                   "area 6, A.3 #495",
	"GET /api/exercises/deploys":                                                                        "area 6, A.3 #496",
	"GET /api/exercises/deploys/:deployID":                                                              "area 6, A.3 #498",
	"GET /api/exercises/deploys/:deployID/dossier":                                                      "area 6, A.3 #502",
	"GET /api/exercises/elevations":                                                                     "area 6, A.3 #506",
	"GET /api/exercises/elevations/:elevationID":                                                        "area 6, A.3 #507",
	"GET /api/exercises/elevations/pending-count":                                                       "area 6, A.3 #510",
	"GET /api/exercises/export/jobs/:jobID":                                                             "area 6, A.3 #512",
	"GET /api/exercises/files/:fileID":                                                                  "area 6, A.3 #514",
	"GET /api/exercises/files/:fileID/chunks/:index":                                                    "area 6, A.3 #515",
	"GET /api/exercises/files/:fileID/meta":                                                             "area 6, A.3 #516",
	"GET /api/exercises/flag-policy":                                                                    "area 6, A.3 #517",
	"GET /api/exercises/import/example":                                                                 "area 6, A.3 #519",
	"GET /api/exercises/import/example/document":                                                        "area 6, A.3 #520",
	"GET /api/exercises/import/jobs":                                                                    "area 6, A.3 #521",
	"GET /api/exercises/import/jobs/:jobID":                                                             "area 6, A.3 #523",
	"GET /api/exercises/import/targets":                                                                 "area 6, A.3 #525",
	"GET /api/exercises/preferences/catalog-columns":                                                    "area 6, A.3 #528",
	"GET /api/exercises/proposals":                                                                      "area 6, A.3 #530",
	"GET /api/exercises/tags":                                                                           "area 6, A.3 #533",
	"GET /api/exercises/test-labs/bookings":                                                             "area 6, A.3 #534",
	"GET /api/exercises/test-labs/room":                                                                 "area 6, A.3 #537",
	"GET /api/exercises/test-runs/lengths":                                                              "area 6, A.3 #538",
	"GET /api/exercises/uploads/:uploadID":                                                              "area 6, A.3 #541",
	"PATCH /api/exercises/:id":                                                                          "area 6, A.3 #452",
	"POST /api/assistant/sessions":                                                                      "area 6, A.3 #82",
	"POST /api/assistant/sessions/:id/attachments":                                                      "area 6, A.3 #85",
	"POST /api/assistant/sessions/:id/messages":                                                         "area 6, A.3 #88",
	"POST /api/assistant/sessions/:id/save":                                                             "area 6, A.3 #89",
	"POST /api/assistant/sessions/:id/start":                                                            "area 6, A.3 #90",
	"POST /api/assistant/sessions/:id/stop":                                                             "area 6, A.3 #91",
	"POST /api/exercises":                                                                               "area 6, A.3 #449",
	"POST /api/exercises/:id/archive":                                                                   "area 6, A.3 #454",
	"POST /api/exercises/:id/checkpoints":                                                               "area 6, A.3 #455",
	"POST /api/exercises/:id/elevation":                                                                 "area 6, A.3 #461",
	"POST /api/exercises/:id/files/lookup":                                                              "area 6, A.3 #462",
	"POST /api/exercises/:id/images":                                                                    "area 6, A.3 #463",
	"POST /api/exercises/:id/images/blobs":                                                              "area 6, A.3 #464",
	"POST /api/exercises/:id/images/blobs/missing":                                                      "area 6, A.3 #466",
	"POST /api/exercises/:id/images/check":                                                              "area 6, A.3 #467",
	"POST /api/exercises/:id/images/service":                                                            "area 6, A.3 #468",
	"POST /api/exercises/:id/proposals":                                                                 "area 6, A.3 #469",
	"POST /api/exercises/:id/publish":                                                                   "area 6, A.3 #470",
	"POST /api/exercises/:id/unarchive":                                                                 "area 6, A.3 #471",
	"POST /api/exercises/:id/versions/:versionID/restore":                                               "area 6, A.3 #476",
	"POST /api/exercises/:id/versions/:versionID/rollback":                                              "area 6, A.3 #477",
	"POST /api/exercises/:id/versions/:versionID/variants/:variantID/deploy":                            "area 6, A.3 #478",
	"POST /api/exercises/:id/versions/:versionID/variants/:variantID/measure":                           "area 6, A.3 #480",
	"POST /api/exercises/:id/versions/:versionID/variants/:variantID/measure/:runID/stop":               "area 6, A.3 #483",
	"POST /api/exercises/:id/versions/:versionID/variants/:variantID/opponents/:index/try":              "area 6, A.3 #485",
	"POST /api/exercises/:id/versions/:versionID/variants/:variantID/opponents/:index/try/:runID/stop":  "area 6, A.3 #488",
	"POST /api/exercises/:id/versions/:versionID/variants/:variantID/opponents/try":                     "area 6, A.3 #489",
	"POST /api/exercises/bulk/archive":                                                                  "area 6, A.3 #493",
	"POST /api/exercises/bulk/unarchive":                                                                "area 6, A.3 #494",
	"POST /api/exercises/deploys/:deployID/check":                                                       "area 6, A.3 #499",
	"POST /api/exercises/deploys/:deployID/devices/:device/rescue":                                      "area 6, A.3 #500",
	"POST /api/exercises/deploys/:deployID/devices/:device/reset":                                       "area 6, A.3 #501",
	"POST /api/exercises/deploys/:deployID/extend":                                                      "area 6, A.3 #503",
	"POST /api/exercises/deploys/:deployID/link":                                                        "area 6, A.3 #504",
	"POST /api/exercises/deploys/:deployID/tasks/:taskID/run-checks":                                    "area 6, A.3 #505",
	"POST /api/exercises/elevations/:elevationID/approve":                                               "area 6, A.3 #508",
	"POST /api/exercises/elevations/:elevationID/reject":                                                "area 6, A.3 #509",
	"POST /api/exercises/export":                                                                        "area 6, A.3 #511",
	"POST /api/exercises/files":                                                                         "area 6, A.3 #513",
	"POST /api/exercises/import":                                                                        "area 6, A.3 #518",
	"POST /api/exercises/import/preview":                                                                "area 6, A.3 #524",
	"POST /api/exercises/import/upload":                                                                 "area 6, A.3 #526",
	"POST /api/exercises/import/uploaded":                                                               "area 6, A.3 #527",
	"POST /api/exercises/proposals/:proposalID/approve":                                                 "area 6, A.3 #531",
	"POST /api/exercises/proposals/:proposalID/reject":                                                  "area 6, A.3 #532",
	"POST /api/exercises/test-labs/bookings":                                                            "area 6, A.3 #535",
	"POST /api/exercises/uploads":                                                                       "area 6, A.3 #539",
	"POST /api/exercises/uploads/:uploadID/complete":                                                    "area 6, A.3 #543",
	"PUT /api/exercises/:id/access":                                                                     "area 6, A.3 #453",
	"PUT /api/exercises/:id/draft":                                                                      "area 6, A.3 #458",
	"PUT /api/exercises/bulk/access":                                                                    "area 6, A.3 #492",
	"PUT /api/exercises/preferences/catalog-columns":                                                    "area 6, A.3 #529",
	"PUT /api/exercises/uploads/:uploadID/chunks/:index":                                                "area 6, A.3 #542",
	// P1b area 7: infrastructure, settings, mail, notifications
	"DELETE /api/infrastructure/agents/:agentID":                                                      "area 7, A.3 #546",
	"DELETE /api/infrastructure/calendar/events/:eventID/reservation":                                 "area 7, A.3 #558",
	"DELETE /api/mail/settings/providers/:providerId":                                                 "area 7, A.3 #593",
	"DELETE /api/notifications/site-banners/:bannerID":                                                "area 7, A.3 #649",
	"DELETE /api/notifications/templates/custom/:templateID":                                          "area 7, A.3 #654",
	"DELETE /api/notifications/templates/email/:id":                                                   "area 7, A.3 #659",
	"DELETE /api/notifications/templates/email/block-presets/:id":                                     "area 7, A.3 #666",
	"DELETE /api/notifications/templates/inapp/:id":                                                   "area 7, A.3 #676",
	"DELETE /api/settings/archive-limits":                                                             "area 7, A.3 #603",
	"DELETE /api/settings/image-registry":                                                             "area 7, A.3 #606",
	"DELETE /api/settings/node-profiles":                                                              "area 7, A.3 #611",
	"DELETE /api/settings/retention":                                                                  "area 7, A.3 #617",
	"DELETE /api/settings/test-runs":                                                                  "area 7, A.3 #620",
	"GET /api/infrastructure/agents":                                                                  "area 7, A.3 #544",
	"GET /api/infrastructure/agents/:agentID/delete-preview":                                          "area 7, A.3 #549",
	"GET /api/infrastructure/calendar/alarms":                                                         "area 7, A.3 #553",
	"GET /api/infrastructure/calendar/capacity":                                                       "area 7, A.3 #555",
	"GET /api/infrastructure/calendar/change-requests":                                                "area 7, A.3 #556",
	"GET /api/infrastructure/calendar/events/:eventID/reservation":                                    "area 7, A.3 #559",
	"GET /api/infrastructure/calendar/settings":                                                       "area 7, A.3 #563",
	"GET /api/infrastructure/calendar/stats":                                                          "area 7, A.3 #565",
	"GET /api/infrastructure/calendar/timeline":                                                       "area 7, A.3 #566",
	"GET /api/infrastructure/monitoring/capacity/current":                                             "area 7, A.3 #567",
	"GET /api/infrastructure/monitoring/capacity/observations":                                        "area 7, A.3 #568",
	"GET /api/infrastructure/monitoring/current":                                                      "area 7, A.3 #569",
	"GET /api/infrastructure/monitoring/observations":                                                 "area 7, A.3 #570",
	"GET /api/infrastructure/stands":                                                                  "area 7, A.3 #571",
	"GET /api/infrastructure/stands/:eventID/:teamID/detail":                                          "area 7, A.3 #574",
	"GET /api/infrastructure/stands/events":                                                           "area 7, A.3 #577",
	"GET /api/infrastructure/stands/facets":                                                           "area 7, A.3 #578",
	"GET /api/infrastructure/status":                                                                  "area 7, A.3 #579",
	"GET /api/infrastructure/summary":                                                                 "area 7, A.3 #580",
	"GET /api/infrastructure/test-labs":                                                               "area 7, A.3 #581",
	"GET /api/infrastructure/test-labs/:labID/detail":                                                 "area 7, A.3 #582",
	"GET /api/infrastructure/test-labs/facets":                                                        "area 7, A.3 #587",
	"GET /api/mail/settings":                                                                          "area 7, A.3 #588",
	"GET /api/notifications/banners":                                                                  "area 7, A.3 #623",
	"GET /api/notifications/broadcasts":                                                               "area 7, A.3 #625",
	"GET /api/notifications/broadcasts/:broadcastID":                                                  "area 7, A.3 #627",
	"GET /api/notifications/broadcasts/:broadcastID/deliveries":                                       "area 7, A.3 #629",
	"GET /api/notifications/broadcasts/:broadcastID/deliveries/export":                                "area 7, A.3 #630",
	"GET /api/notifications/dispatches":                                                               "area 7, A.3 #632",
	"GET /api/notifications/dispatches/:id":                                                           "area 7, A.3 #633",
	"GET /api/notifications/dispatches/facets":                                                        "area 7, A.3 #634",
	"GET /api/notifications/inbox":                                                                    "area 7, A.3 #636",
	"GET /api/notifications/pulse":                                                                    "area 7, A.3 #640",
	"GET /api/notifications/settings/global":                                                          "area 7, A.3 #641",
	"GET /api/notifications/settings/user":                                                            "area 7, A.3 #643",
	"GET /api/notifications/signal-defaults":                                                          "area 7, A.3 #645",
	"GET /api/notifications/site-banners":                                                             "area 7, A.3 #647",
	"GET /api/notifications/stats":                                                                    "area 7, A.3 #651",
	"GET /api/notifications/templates/custom":                                                         "area 7, A.3 #652",
	"GET /api/notifications/templates/custom/:templateID":                                             "area 7, A.3 #655",
	"GET /api/notifications/templates/email":                                                          "area 7, A.3 #657",
	"GET /api/notifications/templates/email/:id":                                                      "area 7, A.3 #660",
	"GET /api/notifications/templates/email/block-presets":                                            "area 7, A.3 #664",
	"GET /api/notifications/templates/email/block-presets/:id":                                        "area 7, A.3 #667",
	"GET /api/notifications/templates/email/brand/logo":                                               "area 7, A.3 #669",
	"GET /api/notifications/templates/email/images/:fileID":                                           "area 7, A.3 #671",
	"GET /api/notifications/templates/email/latest":                                                   "area 7, A.3 #672",
	"GET /api/notifications/templates/inapp":                                                          "area 7, A.3 #674",
	"GET /api/notifications/templates/inapp/:id":                                                      "area 7, A.3 #677",
	"GET /api/notifications/templates/inapp/latest":                                                   "area 7, A.3 #681",
	"GET /api/notifications/types":                                                                    "area 7, A.3 #683",
	"GET /api/notifications/types/:type":                                                              "area 7, A.3 #684",
	"GET /api/settings":                                                                               "area 7, A.3 #599",
	"GET /api/settings/:key":                                                                          "area 7, A.3 #600",
	"GET /api/settings/:key/value":                                                                    "area 7, A.3 #602",
	"GET /api/settings/archive-limits":                                                                "area 7, A.3 #604",
	"GET /api/settings/image-registry":                                                                "area 7, A.3 #607",
	"GET /api/settings/node-profiles":                                                                 "area 7, A.3 #612",
	"GET /api/settings/participant-preview":                                                           "area 7, A.3 #614",
	"GET /api/settings/participant-preview/catalog":                                                   "area 7, A.3 #616",
	"GET /api/settings/retention":                                                                     "area 7, A.3 #618",
	"GET /api/settings/test-runs":                                                                     "area 7, A.3 #621",
	"PATCH /api/mail/settings/providers/:providerId/enabled":                                          "area 7, A.3 #595",
	"PATCH /api/notifications/banners/:id/dismiss":                                                    "area 7, A.3 #624",
	"PATCH /api/notifications/inbox/:id/read":                                                         "area 7, A.3 #637",
	"PATCH /api/notifications/inbox/read-all":                                                         "area 7, A.3 #639",
	"POST /api/infrastructure/agents":                                                                 "area 7, A.3 #545",
	"POST /api/infrastructure/agents/:agentID/check":                                                  "area 7, A.3 #548",
	"POST /api/infrastructure/agents/:agentID/reconnect":                                              "area 7, A.3 #550",
	"POST /api/infrastructure/agents/:agentID/renew-certificate":                                      "area 7, A.3 #551",
	"POST /api/infrastructure/agents/:agentID/rotate-access-key":                                      "area 7, A.3 #552",
	"POST /api/infrastructure/calendar/alarms/:alarmID/acknowledge":                                   "area 7, A.3 #554",
	"POST /api/infrastructure/calendar/change-requests/:requestID/decision":                           "area 7, A.3 #557",
	"POST /api/infrastructure/calendar/recheck":                                                       "area 7, A.3 #561",
	"POST /api/infrastructure/calendar/reservations/:reservationID/replan":                            "area 7, A.3 #562",
	"POST /api/infrastructure/stands/:eventID/:teamID/challenges/:challengeID/devices/:device/rescue": "area 7, A.3 #572",
	"POST /api/infrastructure/stands/:eventID/:teamID/challenges/:challengeID/devices/:device/reset":  "area 7, A.3 #573",
	"POST /api/infrastructure/stands/:eventID/:teamID/recreate":                                       "area 7, A.3 #575",
	"POST /api/infrastructure/stands/bulk/recreate":                                                   "area 7, A.3 #576",
	"POST /api/infrastructure/test-labs/:labID/devices/:device/rescue":                                "area 7, A.3 #583",
	"POST /api/infrastructure/test-labs/:labID/devices/:device/reset":                                 "area 7, A.3 #584",
	"POST /api/infrastructure/test-labs/:labID/terminate":                                             "area 7, A.3 #585",
	"POST /api/infrastructure/test-labs/bulk/terminate":                                               "area 7, A.3 #586",
	"POST /api/mail/settings/footer/preview":                                                          "area 7, A.3 #590",
	"POST /api/mail/settings/providers":                                                               "area 7, A.3 #592",
	"POST /api/mail/settings/providers/:providerId/test":                                              "area 7, A.3 #596",
	"POST /api/mail/settings/providers/test":                                                          "area 7, A.3 #598",
	"POST /api/notifications/broadcasts":                                                              "area 7, A.3 #626",
	"POST /api/notifications/broadcasts/:broadcastID/cancel":                                          "area 7, A.3 #628",
	"POST /api/notifications/broadcasts/audience-count":                                               "area 7, A.3 #631",
	"POST /api/notifications/dispatches/resend":                                                       "area 7, A.3 #635",
	"POST /api/notifications/inbox/:id/resolve":                                                       "area 7, A.3 #638",
	"POST /api/notifications/site-banners":                                                            "area 7, A.3 #648",
	"POST /api/notifications/templates/custom":                                                        "area 7, A.3 #653",
	"POST /api/notifications/templates/email":                                                         "area 7, A.3 #658",
	"POST /api/notifications/templates/email/:id/publish":                                             "area 7, A.3 #662",
	"POST /api/notifications/templates/email/:id/rollback":                                            "area 7, A.3 #663",
	"POST /api/notifications/templates/email/block-presets":                                           "area 7, A.3 #665",
	"POST /api/notifications/templates/email/images":                                                  "area 7, A.3 #670",
	"POST /api/notifications/templates/email/preview":                                                 "area 7, A.3 #673",
	"POST /api/notifications/templates/inapp":                                                         "area 7, A.3 #675",
	"POST /api/notifications/templates/inapp/:id/publish":                                             "area 7, A.3 #679",
	"POST /api/notifications/templates/inapp/:id/rollback":                                            "area 7, A.3 #680",
	"POST /api/notifications/test":                                                                    "area 7, A.3 #682",
	"POST /api/settings/image-registry/check":                                                         "area 7, A.3 #609",
	"POST /api/settings/image-registry/repositories":                                                  "area 7, A.3 #610",
	"PUT /api/infrastructure/agents/:agentID":                                                         "area 7, A.3 #547",
	"PUT /api/infrastructure/calendar/events/:eventID/reservation":                                    "area 7, A.3 #560",
	"PUT /api/infrastructure/calendar/settings":                                                       "area 7, A.3 #564",
	"PUT /api/mail/settings/footer":                                                                   "area 7, A.3 #589",
	"PUT /api/mail/settings/identity":                                                                 "area 7, A.3 #591",
	"PUT /api/mail/settings/providers/:providerId":                                                    "area 7, A.3 #594",
	"PUT /api/mail/settings/providers/order":                                                          "area 7, A.3 #597",
	"PUT /api/notifications/settings/global":                                                          "area 7, A.3 #642",
	"PUT /api/notifications/settings/user":                                                            "area 7, A.3 #644",
	"PUT /api/notifications/signal-defaults":                                                          "area 7, A.3 #646",
	"PUT /api/notifications/site-banners/:bannerID":                                                   "area 7, A.3 #650",
	"PUT /api/notifications/templates/custom/:templateID":                                             "area 7, A.3 #656",
	"PUT /api/notifications/templates/email/:id":                                                      "area 7, A.3 #661",
	"PUT /api/notifications/templates/email/block-presets/:id":                                        "area 7, A.3 #668",
	"PUT /api/notifications/templates/inapp/:id":                                                      "area 7, A.3 #678",
	"PUT /api/settings/:key":                                                                          "area 7, A.3 #601",
	"PUT /api/settings/archive-limits":                                                                "area 7, A.3 #605",
	"PUT /api/settings/image-registry":                                                                "area 7, A.3 #608",
	"PUT /api/settings/node-profiles":                                                                 "area 7, A.3 #613",
	"PUT /api/settings/participant-preview":                                                           "area 7, A.3 #615",
	"PUT /api/settings/retention":                                                                     "area 7, A.3 #619",
	"PUT /api/settings/test-runs":                                                                     "area 7, A.3 #622",
}

func TestEveryRouteDeclaresKeys(t *testing.T) {
	mounted := map[string]bool{}
	for _, s := range buildRoutePolicy(t) {
		id := s.method + " " + s.path
		mounted[id] = true
		_, listed := undeclaredRoutes[id]
		switch {
		case s.decl == nil && !listed:
			t.Errorf("%s declares no keys: wrap its gate in Route(authz.Decl, gate) per Appendix A.3 of the access spec", id)
		case s.decl != nil && listed:
			t.Errorf("%s declares %s but is still in undeclaredRoutes: drop the entry (the list only shrinks)", id, s.decl)
		}
	}
	for id := range undeclaredRoutes {
		if !mounted[id] {
			t.Errorf("undeclaredRoutes lists %s, which is not mounted", id)
		}
	}
}

// legacyDivergences are the declared routes whose keys do not map back to today's gate on purpose: the change
// Appendix A.3 asks for (spec §7.2 lists it), shown here so a reviewer reads it as a rule change. Keyed like
// undeclaredRoutes, with the A.3 row and the reason.
var legacyDivergences = map[string]string{
	"GET /api/admin/assistant/statistics/events/:eventID": "A.3.3 #50: any-of cyberbrain.stats.read, events.read; today platform.settings.read only. The event card reader sees the per-event statistics source (spec §7.2, assistant statistics row; §9 Q14)",
	"GET /api/admin/dashboard":                            "A.3.3 #54: any-of the block keys; today self + the hard-coded IsAdminTier() (spec §7.2: IsAdminTier() becomes \"holds any admin-area key\"; A.4.1 item 10)",
}

// TestDeclaredKeysMapToTheLegacyGate: for each declared route, the keys map back (authz.LegacyOf) to the check that
// routes.golden shows today. A key maps when today's permission that decides it gates the route, or when its named
// legacy check is a layer of the route (event.requireManage, ...), or through the platform key that carries it.
// Implicit keys stand for their own permission (self, notifications.self); a public route has no real gate; a
// relation is checked behind a self gate. Disagreement is a failure unless legacyDivergences explains it.
func TestDeclaredKeysMapToTheLegacyGate(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range buildRoutePolicy(t) {
		if s.decl == nil {
			continue
		}
		id := s.method + " " + s.path
		problem := legacyMismatch(s)
		_, excused := legacyDivergences[id]
		if excused {
			seen[id] = true
		}
		switch {
		case problem != "" && !excused:
			t.Errorf("%s: declared %s, legacy gate %v layers %v: %s", id, s.decl, s.gates, s.layers, problem)
		case problem == "" && excused:
			t.Errorf("%s is in legacyDivergences but its keys map to the legacy gate: drop the entry", id)
		}
	}
	for id := range legacyDivergences {
		if !seen[id] {
			t.Errorf("legacyDivergences lists %s, which is not a declared route", id)
		}
	}
}

// legacyMismatch explains why the declaration of s does not map back to its legacy gate, or returns "".
func legacyMismatch(s routeSummary) string {
	d := *s.decl
	var real []rbac.Permission
	for _, g := range s.gates {
		if !rbac.RolePublic.HasPermission(g) {
			real = append(real, g)
		}
	}
	var grantable, implicit, public []authz.Key
	for _, k := range d.Keys() {
		spec, ok := authz.Lookup(k)
		switch {
		case !ok:
			return "key " + string(k) + " is not in the catalogue"
		case spec.Allows(authz.ScopeImplicit):
			implicit = append(implicit, k)
		case spec.Allows(authz.ScopePublic):
			public = append(public, k)
		default:
			grantable = append(grantable, k)
		}
	}

	if d.Scope.Kind() == "public" {
		if len(real) > 0 {
			return "a public declaration on a gated route"
		}
		for _, k := range public {
			if !mapsToGate(k, s.gates, nil) {
				return "public key " + string(k) + " is not today's public gate"
			}
		}
		return ""
	}
	if len(real) == 0 {
		return "the route has no real gate today, but declares " + d.String()
	}

	// Every real gate is explained by a declared key, and every declared key by the gate or a layer.
	layers := authzLayers(s)
	for _, g := range real {
		explained := false
		for _, k := range d.Keys() {
			l, _ := authz.LegacyOf(k)
			if slices.Contains(l.Permissions, g) || g == rbac.PermSelf && len(grantable) > 0 && mapsToLayer(k, layers) {
				explained = true
			}
		}
		if g == rbac.PermSelf && d.Relation != "" {
			explained = true // the relation is checked behind the self gate, in the use case
		}
		if !explained {
			return "today's gate " + string(g) + " is decided by none of the declared keys"
		}
	}
	for _, k := range append(implicit, grantable...) {
		if !mapsToGate(k, real, layers) {
			return "key " + string(k) + " does not map back to today's gate or layers"
		}
	}
	return ""
}

func mapsToGate(k authz.Key, gates []rbac.Permission, layers []string) bool {
	l, ok := authz.LegacyOf(k)
	if !ok {
		return false
	}
	for _, g := range gates {
		if slices.Contains(l.Permissions, g) {
			return true
		}
	}
	if mapsToLayer(k, layers) {
		return true
	}
	for _, carrier := range l.CarriedBy {
		if mapsToGate(carrier, gates, layers) {
			return true
		}
	}
	return false
}

// mapsToLayer: the named legacy check of k is one of the route's layers (event.requireManage → requireManage).
func mapsToLayer(k authz.Key, layers []string) bool {
	l, ok := authz.LegacyOf(k)
	if !ok || l.Check == "" || l.Check == authz.CheckNew {
		return false
	}
	for _, layer := range layers {
		if strings.HasSuffix(layer, "."+string(l.Check)) || layer == string(l.Check) {
			return true
		}
	}
	return false
}

func TestLegacyMismatchCatchesDisagreement(t *testing.T) {
	decl := func(d authz.Decl) *authz.Decl { return &d }
	cases := []struct {
		name  string
		s     routeSummary
		agree bool
	}{
		{"platform key on its permission", routeSummary{gates: []rbac.Permission{rbac.PermUsersRead}, decl: decl(authz.AnyOf(authz.SourcePlatform, authz.UsersList))}, true},
		{"platform key on another permission", routeSummary{gates: []rbac.Permission{rbac.PermUsersRead}, decl: decl(authz.AnyOf(authz.SourcePlatform, authz.UsersDelete))}, false},
		{"one any-of key off the gate", routeSummary{gates: []rbac.Permission{rbac.PermUsersRead}, decl: decl(authz.AnyOf(authz.SourcePlatform, authz.UsersList, authz.EventsRead))}, false},
		{"own data on self", routeSummary{gates: []rbac.Permission{rbac.PermSelf}, decl: decl(authz.OwnData())}, true},
		{"own data on a permission", routeSummary{gates: []rbac.Permission{rbac.PermUsersRead}, decl: decl(authz.OwnData())}, false},
		{"public flow without a gate", routeSummary{decl: decl(authz.PublicRoute(""))}, true},
		{"public declaration on a gated route", routeSummary{gates: []rbac.Permission{rbac.PermSelf}, decl: decl(authz.PublicRoute(""))}, false},
		{"keys on an ungated route", routeSummary{decl: decl(authz.AnyOf(authz.SourcePlatform, authz.UsersList))}, false},
		{"event key on its layer", routeSummary{gates: []rbac.Permission{rbac.PermSelf}, layers: []string{"event.requireManage"}, decl: decl(authz.AnyOf(authz.SourceEventParam("id"), authz.EventSettingsUpdate))}, true},
		{"event key without its layer", routeSummary{gates: []rbac.Permission{rbac.PermSelf}, decl: decl(authz.AnyOf(authz.SourceEventParam("id"), authz.EventSettingsUpdate))}, false},
		{"event key carried by the platform key", routeSummary{gates: []rbac.Permission{rbac.PermEventsWrite}, decl: decl(authz.AnyOf(authz.SourceEventParam("id"), authz.EventsArchive, authz.EventArchive))}, true},
		{"relation behind self", routeSummary{gates: []rbac.Permission{rbac.PermSelf}, decl: decl(authz.Related(authz.SourceTenant, authz.RelParticipant))}, true},
	}
	for _, c := range cases {
		got := legacyMismatch(c.s)
		if (got == "") != c.agree {
			t.Errorf("%s: legacyMismatch = %q, want agreement %v", c.name, got, c.agree)
		}
	}
}
