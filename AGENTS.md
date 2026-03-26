# Repository Guidelines

## Project Structure & Module Organization
The repository is script-first. Root-level Python entry points handle collection and processing: `monitor.py` and `monitor_with_images.py` capture group chat history, `scrape_weibo_user.py` and `scrape_all_weibo.py` collect user posts, and `browser_fetch.py` / `get_raw_image_msg.py` are targeted fetch helpers. Data-processing output follows a simple pipeline: `clean_history.jsonl` -> `data_clean.py` -> `cleaned_data.jsonl` -> `generate_dashboard_data.py` -> `dashboard_data.json`, rendered by `dashboard.html`. Treat `weibo_user_data/` as runtime state for Playwright login sessions, not source. `weibo_monitor_go/` is a separate Go prototype with `config/` and `weibo/` packages.

## Build, Test, and Development Commands
- `source .venv/bin/activate`: use the local Python environment before running scripts.
- `python scrape_weibo_user.py`: fetch recent posts for the configured Weibo UID.
- `python monitor.py`: capture chat history into `clean_history.jsonl`.
- `python data_clean.py`: normalize and enrich raw chat records.
- `python generate_dashboard_data.py`: rebuild `dashboard_data.json` for `dashboard.html`.
- `cd weibo_monitor_go && go test ./...`: smoke-test Go package changes when working in the prototype module.

## Coding Style & Naming Conventions
Use 4-space indentation in Python, `snake_case` for functions and files, and `UPPER_CASE` for module constants such as `STOP_CONDITION`. Keep script entry points under `if __name__ == "__main__":`. In Go, rely on `gofmt`, keep package names lowercase, and use `CamelCase` for exported types and methods. Prefer small helper functions over long inline branches, especially around parsing and media extraction.

## Testing Guidelines
There is no committed Python test suite yet, so verify changes by running the affected script on a small local sample and inspecting the generated JSONL or JSON output. Add deterministic tests for new parsing logic where practical: Python tests should follow `test_<behavior>.py`; Go tests should live beside the package as `*_test.go` and use `TestXxx` names.

## Commit & Pull Request Guidelines
Git history is not available in this workspace, so no repository-specific commit convention can be inferred. Use short imperative subjects such as `Add media dedupe for chat exports`. In pull requests, list the scripts changed, note any generated files that must be refreshed, describe manual verification steps, and include a screenshot when `dashboard.html` output changes.

## Security & Configuration Tips
Do not commit live browser session data from `weibo_user_data/`, Telegram secrets from `weibo_monitor_go/config.yaml`, or personal JSONL exports unless the change intentionally updates sanitized fixtures.
