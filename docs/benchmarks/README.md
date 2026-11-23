# s390x benchmark reports

`report_*.json` and `report_*.md` files in this directory are machine-generated
by the automated daily s390x benchmark job. They are archived in a single
commit (`chore(telemetry): archive s390x daily benchmark reports`) to keep
main's history focused on development.

These patterns are git-ignored: future daily reports are written to the local
bench archive (`/root/bench-archive/`) and are not committed to main.
