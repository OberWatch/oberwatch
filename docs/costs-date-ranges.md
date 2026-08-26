# Costs date ranges and time buckets

This describes the windows the Costs page can select, the instants it sends to the
API, and how the stacked time series is bucketed.

## Range boundaries

Every window is defined on the viewer's local calendar and sent to the API as two
exact UTC instants (`from` and `to`, RFC 3339). One resolved window drives the
total, both bar charts, the stacked series, the table and the CSV export, so all
of them always describe the same slice of time.

| Range | `from` | `to` |
| --- | --- | --- |
| Today | Start of the current local calendar day | The current instant |
| 7d | Start of the local calendar day six days before today | The current instant |
| 30d | Start of the local calendar day twenty-nine days before today | The current instant |
| Custom | Start of the local calendar day named by the start input | Last millisecond of the local calendar day named by the end input |

Notes:

- Today is a calendar day, not a rolling 24 hours. At 23:00 local the window is 23
  hours long; at 00:30 local it is 30 minutes long.
- 7d and 30d are inclusive calendar-day counts that include today, so 7d is today
  plus the six days before it.
- Day offsets use calendar arithmetic, not multiples of 24 hours. A window that
  spans a daylight-saving transition still starts at a real local midnight.
- Custom start and end are both inclusive whole local days.

## Custom range validation

The custom inputs are a draft until Apply is pressed, so a half-entered range
never triggers a query. Apply rejects, in this order:

| Condition | Message |
| --- | --- |
| Start empty | `Select a start date.` |
| End empty | `Select an end date.` |
| Start not a real date | `Enter a valid start date.` |
| End not a real date | `Enter a valid end date.` |
| Start after end | `Start date must be on or before the end date.` |

A rejected range leaves the last loaded data on screen and shows the message
beside the inputs.

## Time buckets

The stacked series is always fetched with `group_by=agent_hour`, which keeps both
the agent and the time bucket and returns each bucket as an absolute UTC instant.

Ranges of two calendar days or fewer are charted hourly. Longer ranges are folded
into local calendar days **on the client**, by reading each hourly instant through
the viewer's calendar.

Day folding is deliberately not done in SQL. A day truncated from a UTC timestamp
is not a calendar day for anyone outside UTC:

- In a negative UTC offset one local day spans two UTC dates, so a single local
  day would be split across two buckets and the chart's first and last columns
  would be partial.
- Daylight-saving days are 23 or 25 hours long, which a fixed offset cannot model.

For the same reason the API has no `agent_day` grouping. The pre-existing `hour`
and `day` groupings still aggregate in UTC; `hour` is an absolute instant and is
safe to re-bucket on any calendar, `day` is a UTC date.

## Concurrency

Each load claims a sequence number. If the user changes the range before a load
answers, the older load reports itself stale and its response is discarded, so a
slow request cannot overwrite a newer selection. Retry re-runs the current
selection, including custom dates.
