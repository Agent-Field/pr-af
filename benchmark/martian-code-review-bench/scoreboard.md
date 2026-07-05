# GLM-5.2 + PR-AF — Martian Code-Review-Bench scoreboard

Scored 38 problems · micro-recall 72/102 = **0.706** · macro-recall **0.729** · judge=anthropic/claude-sonnet-4.6 · severity-agnostic (HIT = bug found)

| problem | repo | diff | goldens | hits | recall | findings | missed |
|---|---|---|---|---|---|---|---|
| keycloak#36882 | keycloak | 0.9262 | 1 | 1 | **1.0** | 25 | — |
| sentry#95633 | sentry | 0.9235 | 3 | 1 | **0.333** | 25 | The queue.shutdown() method with 'immediate=False' parameter may not exist in th |
| grafana#107534 | grafana | 0.8934 | 1 | 1 | **1.0** | 10 | — |
| cal_dot_com#22345 | cal_dot_com | 0.8402 | 2 | 2 | **1.0** | 16 | — |
| grafana#103633 | grafana | 0.8033 | 2 | 1 | **0.5** | 10 | The test comment says the cached permissions 'allow access', but the map stores  |
| grafana#76186 | grafana | 0.8033 | 2 | 0 | **0.0** | 25 | The ContextualLoggerMiddleware methods (QueryData, CallResource, CheckHealth, Co |
| cal_dot_com#10600 | cal_dot_com | 0.75 | 4 | 3 | **0.75** | 25 | Error message mentions 'backup code login' but this is a disable endpoint, not l |
| keycloak#36880 | keycloak | 0.7268 | 3 | 3 | **1.0** | 25 | — |
| keycloak#37429 | keycloak | 0.7152 | 4 | 4 | **1.0** | 25 | — |
| grafana#79265 | grafana | 0.7131 | 5 | 4 | **0.8** | 25 | Time window calculation inconsistency: Using device.UpdatedAt.UTC().Add(-anonymo |
| sentry#93824 | sentry | 0.7066 | 5 | 3 | **0.6** | 14 | Inconsistent metric tagging with 'shard' and 'shards'; Breaking out of the loop  |
| keycloak#32918 | keycloak | 0.6967 | 2 | 2 | **1.0** | 17 | — |
| sentry#77754 | sentry | 0.6824 | 4 | 3 | **0.75** | 25 | Method name says 'empty_array' but tests empty dict - consider renaming to 'test |
| sentry#80528 | sentry | 0.6639 | 2 | 1 | **0.5** | 16 | The code fetches MonitorCheckIn objects by ID when the required data already exi |
| keycloak#38446 | keycloak | 0.6475 | 2 | 1 | **0.5** | 24 | After creating the RecoveryAuthnCodesCredentialModel, consider setting its id fr |
| sentry#67876 | sentry | 0.6366 | 3 | 1 | **0.333** | 12 | Null reference if github_authenticated_user state is missing; OAuth state uses p |
| cal_dot_com#10967 | cal_dot_com | 0.6082 | 5 | 3 | **0.6** | 25 | Potential null reference if mainHostDestinationCalendar is undefined if evt.dest |
| grafana#106778 | grafana | 0.5984 | 2 | 1 | **0.5** | 25 | The rendered GrafanaRuleListItem is missing the required key prop for React list |
| keycloak#33832 | keycloak | 0.5656 | 2 | 1 | **0.5** | 25 | Dead code exists where ASN1Encoder instances are created and written to, but the |
| keycloak#37634 | keycloak | 0.5553 | 4 | 1 | **0.25** | 25 | Wrong parameter in null check (grantType vs. rawTokenId); Javadoc mentions "usua |
| sentry#94376 | sentry | 0.5328 | 3 | 2 | **0.667** | 25 | Using Python’s built-in hash() to build cache keys is non-deterministic across p |
| grafana#90939 | grafana | 0.5205 | 2 | 1 | **0.5** | 5 | In addition to the missing double-check, the function has a critical flaw in its |
| sentry#80168 | sentry | 0.5205 | 2 | 2 | **1.0** | 17 | — |
| cal_dot_com#14740 | cal_dot_com | 0.5164 | 5 | 3 | **0.6** | 25 | uniqueGuests filters out existing attendees and blacklisted emails but does not  |
| cal_dot_com#22532 | cal_dot_com | 0.5123 | 2 | 2 | **1.0** | 25 | — |
| grafana#97529 | grafana | 0.5041 | 2 | 2 | **1.0** | 25 | — |
| cal_dot_com#8087 | cal_dot_com | 0.4795 | 2 | 2 | **1.0** | 24 | — |
| cal_dot_com#14943 | cal_dot_com | 0.3934 | 2 | 2 | **1.0** | 12 | — |
| cal_dot_com#7232 | cal_dot_com | 0.377 | 2 | 2 | **1.0** | 25 | — |
| grafana#80329 | grafana | 0.3443 | 1 | 1 | **1.0** | 12 | — |
| sentry#92393 | sentry | 0.3388 | 3 | 0 | **0.0** | 17 | OptimizedCursorPaginator negative-offset branch slices QuerySet with a negative  |
| grafana#94942 | grafana | 0.3361 | 2 | 2 | **1.0** | 16 | — |
| cal_dot_com#11059 | cal_dot_com | 0.3361 | 5 | 5 | **1.0** | 25 | — |
| keycloak#37038 | keycloak | 0.3279 | 2 | 2 | **1.0** | 25 | — |
| keycloak#40940 | keycloak | 0.3238 | 2 | 2 | **1.0** | 25 | — |
| grafana#90045 | grafana | 0.3115 | 3 | 3 | **1.0** | 25 | — |
| keycloak#41249 | keycloak | 0.2377 | 2 | 0 | **0.0** | 25 | ConditionalPasskeysEnabled() called without UserModel parameter; With isConditio |
| cal_dot_com#8330 | cal_dot_com | 0.1967 | 2 | 2 | **1.0** | 25 | — |
