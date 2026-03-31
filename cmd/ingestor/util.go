package main

import "time"

func unixTime(epoch int64) time.Time {
	return time.Unix(epoch, 0)
}
