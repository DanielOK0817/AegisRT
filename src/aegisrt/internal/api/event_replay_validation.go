package api

import "aegisrt/internal/eventlog"

func validateEventReplay(afterID uint64, limit int, replay eventlog.Replay) error {
	if limit <= 0 || replay.LastAvailableID == 0 {
		if limit <= 0 || replay.FirstAvailableID != 0 || len(replay.Events) != 0 ||
			replay.Gap || replay.GapFromID != 0 || replay.GapThroughID != 0 ||
			replay.NextAfterID != afterID || replay.HasMore {
			return errInvalidEventProjection
		}
		return nil
	}
	if replay.FirstAvailableID == 0 || replay.FirstAvailableID > replay.LastAvailableID ||
		afterID > replay.LastAvailableID {
		return errInvalidEventProjection
	}

	expectedGap := afterID < replay.FirstAvailableID-1
	if replay.Gap != expectedGap {
		return errInvalidEventProjection
	}
	if expectedGap {
		if replay.GapFromID != afterID+1 || replay.GapThroughID != replay.FirstAvailableID-1 {
			return errInvalidEventProjection
		}
	} else if replay.GapFromID != 0 || replay.GapThroughID != 0 {
		return errInvalidEventProjection
	}

	expectedFirst := afterID + 1
	remaining := uint64(0)
	if afterID < replay.LastAvailableID {
		if expectedFirst < replay.FirstAvailableID {
			expectedFirst = replay.FirstAvailableID
		}
		remaining = replay.LastAvailableID - expectedFirst + 1
	}
	expectedCount := limit
	if remaining < uint64(expectedCount) {
		expectedCount = int(remaining)
	}
	if len(replay.Events) != expectedCount {
		return errInvalidEventProjection
	}
	for index, event := range replay.Events {
		if event.ID != expectedFirst+uint64(index) {
			return errInvalidEventProjection
		}
	}
	expectedNext := afterID
	if expectedCount > 0 {
		expectedNext = expectedFirst + uint64(expectedCount) - 1
	}
	if replay.NextAfterID != expectedNext || replay.HasMore != (remaining > uint64(expectedCount)) {
		return errInvalidEventProjection
	}
	return nil
}
