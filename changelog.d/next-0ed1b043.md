### Added

- PiG detects the Rex terminal (`TERM_PROGRAM=rex`) and uses OSC 8 hyperlinks and true color there; images stay off because Rex only partly implements the Kitty graphics protocol. The check runs before the Kitty variables, so a leaked `KITTY_WINDOW_ID` does not turn images on (Pi 0ed1b043, #11131).
