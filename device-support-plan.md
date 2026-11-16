# ThinQ Device Support Plan

## Sources and scope

- [LG ThinQ API](https://smartsolution.developer.lge.com/en/apiManage/thinq_connect)
- [LG device profiles](https://smartsolution.developer.lge.com/en/apiManage/device_profile)
- [ThinQ Connect Python SDK](https://github.com/thinq-connect/pythinqconnect)

LG defines profiles per device type and model. Some products are read-only; others require a location, multiple properties in one request, or a particular operating state. The personal access token API has regional and account restrictions. Compatibility cannot be guaranteed for every physical model by mocks alone.

## Test-driven implementation

1. Add mocked discovery, profile and state fixtures for the documented device families, including object, array, multiple-location and combined WashTower responses. Preserve unknown devices and missing or unavailable states without inventing readings.
2. Derive read-only capabilities from actual state and writable controls from profile permissions and documented value constraints. Reject missing, malformed, read-only or invalid values; keep unknown control shapes disabled.
3. Implement and test device-specific command requirements, including location, remote-control locks, coupled hood and microwave controls, and conditional air conditioner and purifier states. Verify exact request bodies with a mock ThinQ server.
4. Update setup, device tab and both widgets to display actual state, supported controls, offline and read-only devices. Test interactions and responsive layouts with mocked API responses.
5. Run Go tests and vet, JavaScript and browser tests, manifest validation, and release checks. Document supported behavior and remaining model-specific limitations before bumping versions, committing, pushing and releasing.

## Safety and verification

Do not send commands to real devices during testing. Never claim that a profile permission alone establishes that a command is valid in every device state. Keep unsupported combinations read-only and report LG API failures to the user. Test representative real devices separately before claiming model-level coverage.

## Current status

- Documented object and array state examples are covered by mapping tests, except the washer, which uses a dedicated mapping and its own tests.
- Selected writable enum controls, including ventilation, refrigerator and wine-cellar temperatures, refrigerator power saving, combined laundry operations, and paired hood and microwave controls are supported. Power saving uses a synthetic writable profile based on the documented boolean profile shape and control payload; the bundled refrigerator profile does not include that property. Official WashCombo type names are normalized to the corresponding example families. Laundry and dishwasher starts require reported remote enablement; laundry commands also require a matching writable location.
- Country-to-region defaults match the Python SDK's 162-country mapping, including the offline country-list fallback. Mock HTTP tests verify the SDK-compatible conditional-control header and documented paired and combined payloads.
- Paired sliders stage both visible values for a single Apply action, preserve drafts across unchanged snapshots, and discard drafts when reported values change. Concurrent changes not yet reported by LG can still be overwritten because the API does not provide compare-and-swap semantics.
- Other ranges and booleans, conditional operating-state rules, setup workflows and full browser coverage remain to be implemented and verified. Per-installation client IDs are not generated automatically; changing an existing ID requires a migration strategy for realtime registrations and certificates.
- A release should wait until the remaining command and UI coverage is complete; mock tests cannot establish compatibility with every physical model.