# GLM-5.2 + PR-AF — substantive-golden scoreboard (nits ignored)

Scored 38 · **27/38 problems fully topped** (substantive recall=1.0) · substantive micro-recall 55/73 = **0.753** · macro **0.796** · nits excluded (Low severity).

| problem | diff | substantive | nits | overall | substantive misses |
|---|---|---|---|---|---|
| keycloak#36882 | 0.9262 | ✅ 1/1 (1.0) | 0 | 1.0 | — |
| sentry#95633 | 0.9235 | ⚠️ 0/1 (0.0) | 2 | 0.333 | [High] The queue.shutdown() method with 'immediate=False' parameter may not exist in the standard |
| grafana#107534 | 0.8934 | ✅ 0/0 (1.0) | 1 | 1.0 | — |
| cal_dot_com#22345 | 0.8402 | ✅ 1/1 (1.0) | 1 | 1.0 | — |
| grafana#103633 | 0.8033 | ✅ 1/1 (1.0) | 1 | 0.5 | — |
| grafana#76186 | 0.8033 | ⚠️ 0/1 (0.0) | 1 | 0.0 | [High] The ContextualLoggerMiddleware methods (QueryData, CallResource, CheckHealth, CollectMetri |
| cal_dot_com#10600 | 0.75 | ✅ 2/2 (1.0) | 2 | 0.75 | — |
| keycloak#36880 | 0.7268 | ✅ 3/3 (1.0) | 0 | 1.0 | — |
| keycloak#37429 | 0.7152 | ✅ 2/2 (1.0) | 2 | 1.0 | — |
| grafana#79265 | 0.7131 | ✅ 3/3 (1.0) | 2 | 0.8 | — |
| sentry#93824 | 0.7066 | ⚠️ 0/4 (0.0) | 1 | 0.0 | [Medium] Inconsistent metric tagging with 'shard' and 'shards'; [High] Because flusher processes are created v |
| keycloak#32918 | 0.6967 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| sentry#77754 | 0.6824 | ✅ 2/2 (1.0) | 2 | 0.75 | — |
| sentry#80528 | 0.6639 | ✅ 1/1 (1.0) | 1 | 0.5 | — |
| keycloak#38446 | 0.6475 | ✅ 1/1 (1.0) | 1 | 0.5 | — |
| sentry#67876 | 0.6366 | ⚠️ 1/3 (0.333) | 0 | 0.333 | [Medium] Null reference if github_authenticated_user state is missing; [Medium] OAuth state uses pipeline.sign |
| cal_dot_com#10967 | 0.6082 | ⚠️ 2/3 (0.667) | 2 | 0.6 | [High] Potential null reference if mainHostDestinationCalendar is undefined if evt.destinationCal |
| grafana#106778 | 0.5984 | ⚠️ 1/2 (0.5) | 0 | 0.5 | [Medium] The rendered GrafanaRuleListItem is missing the required key prop for React list items. Th |
| keycloak#33832 | 0.5656 | ✅ 1/1 (1.0) | 1 | 0.5 | — |
| keycloak#37634 | 0.5553 | ⚠️ 1/2 (0.5) | 2 | 0.25 | [Critical] Wrong parameter in null check (grantType vs. rawTokenId) |
| sentry#94376 | 0.5328 | ✅ 1/1 (1.0) | 2 | 0.667 | — |
| grafana#90939 | 0.5205 | ⚠️ 1/2 (0.5) | 0 | 0.5 | [High] In addition to the missing double-check, the function has a critical flaw in its error han |
| sentry#80168 | 0.5205 | ✅ 1/1 (1.0) | 1 | 1.0 | — |
| cal_dot_com#14740 | 0.5164 | ⚠️ 3/4 (0.75) | 1 | 0.6 | [Medium] uniqueGuests filters out existing attendees and blacklisted emails but does not deduplicat |
| cal_dot_com#22532 | 0.5123 | ✅ 1/1 (1.0) | 1 | 1.0 | — |
| grafana#97529 | 0.5041 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| cal_dot_com#8087 | 0.4795 | ✅ 1/1 (1.0) | 1 | 1.0 | — |
| cal_dot_com#14943 | 0.3934 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| cal_dot_com#7232 | 0.377 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| grafana#80329 | 0.3443 | ✅ 0/0 (1.0) | 1 | 1.0 | — |
| sentry#92393 | 0.3388 | ⚠️ 0/3 (0.0) | 0 | 0.0 | [Critical] OptimizedCursorPaginator negative-offset branch slices QuerySet with a negative start inde; [High]  |
| cal_dot_com#11059 | 0.3361 | ✅ 5/5 (1.0) | 0 | 1.0 | — |
| grafana#94942 | 0.3361 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| keycloak#37038 | 0.3279 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| keycloak#40940 | 0.3238 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
| grafana#90045 | 0.3115 | ✅ 3/3 (1.0) | 0 | 1.0 | — |
| keycloak#41249 | 0.2377 | ⚠️ 0/2 (0.0) | 0 | 0.0 | [Medium] ConditionalPasskeysEnabled() called without UserModel parameter; [Medium] With isConditionalPasskeysE |
| cal_dot_com#8330 | 0.1967 | ✅ 2/2 (1.0) | 0 | 1.0 | — |
