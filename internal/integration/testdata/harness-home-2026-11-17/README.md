Home operator Claude Code model calls, 2026-11-17 15:03 to 2026-11-18 18:34:36 UTC (4,392 calls), for #120 criterion 4.
rows.json.gz: one intake row per call (POST /api/v1/cost/external-usage); only token counts, model, ts and origin are kept.
Message ids are hashed (sha256, 16 hex) and session ids cut to 8 characters; no prompt, response or tool text.
harness_10min.csv / harness_10h.csv: the measurement script per bucket and model (list_usd, of_which_subagents_usd).
Source: jmarcone/covey-experiment results/2026-11-18-harness-home/, script scripts/measure/harness_usage.py.
