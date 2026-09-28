# Hosted Linux shards partition make check. Keep them in step with the check prerequisites when adding a gate.
ci-build: build vet lint go-fix-clean
ci-test-fast: test-fast
ci-test-cli: test-cli
ci-race: test-race
ci-integration: test-integration
ci-docs: docs-drift

test-fast: test-prereqs
	@./automation/ci/test-grouped.sh fast

test-cli: test-prereqs
	@./automation/ci/test-grouped.sh cli

.PHONY: ci-build ci-test-fast ci-test-cli ci-race ci-integration ci-docs test-fast test-cli
