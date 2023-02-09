check_graphite
==============

This is a small check program which takes a key and a timeframe, gets the
graphite data and then checks the returned values against the warning and
error levels.
If any one value raises a warning or error, this will be reported back. So in
the timeframe selected, the worst case error is returned.
