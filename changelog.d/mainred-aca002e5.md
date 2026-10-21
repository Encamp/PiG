### Fixed

- CI: `make interface-gaps` passes on main again. The interface mapping ledger had not been regenerated for the chord fork replay test that landed with the previous train, so `autobind -check` reported that the ledger is not what the detector derives; `make interface-gaps-update` rewrites the two stale rows. <!-- fix: aca002e5 -->
- CI: the Windows test jobs no longer fail the `pig extension upgrade` notice tests when the temporary directory holds a `~`, as a runner's 8.3 short name (`RUNNER~1`) does. The notice already quotes such a path for the Windows shell (`"C:\Users\RUNNER~1\..."`) and is unchanged; the tests expected the raw path and now expect the quoted word, as the notice test beside them does. <!-- fix: 6811585d -->
