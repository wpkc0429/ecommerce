DROP TRIGGER design_requests_guard ON design_requests;
DROP FUNCTION design_requests_guard();
DROP TRIGGER design_untracked_changes_guard ON design_untracked_changes;
DROP FUNCTION design_untracked_guard();
DROP TRIGGER design_screenshots_guard ON design_screenshots;
DROP FUNCTION design_screenshots_guard();
DROP TRIGGER design_request_events_append_only ON design_request_events;
DROP FUNCTION design_events_append_only();
DROP FUNCTION design_guard_retention();
