.PHONY: check-agent-rules copyright-check governance-check

check-agent-rules:
	@sh script/check-agent-rules.sh

copyright-check:
	@sh script/check-license-headers.sh

governance-check: check-agent-rules copyright-check
