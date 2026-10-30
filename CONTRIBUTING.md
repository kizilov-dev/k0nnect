# Contributing

Start with an issue describing the concrete Kubernetes/CNI profile or failure.
Never attach kubeconfigs, passwords, private keys or raw bootstrap data.

Run `make check` for Go changes and `make site` for documentation/site changes.
The site has no external build dependencies beyond Python 3. Documentation source
is in `website/content/`; generated `site/` output is ignored.

Changes to network ownership, bootstrap, CSR approval or removal must include
relevant validation. A new CNI or transport needs its own compatibility checks;
passing unit tests alone does not certify live Pod connectivity.

Infrastructure integration tests must target an explicitly authorized disposable
lab. Public test reports should state versions and outcomes without exposing
addresses, credentials or raw diagnostic snapshots.
