// Unit tests for the data and text helpers (no shell, no network).
//
// Run from the repo:  gjs -m claude_usage_indicator/tests/test_usage.js

import {
    barFraction, formatPercent, formatReset, formatUpdated, levelOf, parseLimits, parseTime,
} from '../claude-usage@localhost/usage.js';

// Trimmed copy of a real reply from the usage endpoint.
const SAMPLE = {
    seven_day: {utilization: 71.0, resets_at: '2026-11-21T16:00:00.113116+00:00'},
    limits: [
        {
            kind: 'weekly_all', group: 'weekly', percent: 71, severity: 'normal',
            resets_at: '2026-11-21T16:00:00.113116+00:00', scope: null,
        },
        {
            kind: 'weekly_scoped', group: 'weekly', percent: 76, severity: 'warning',
            resets_at: '2026-11-21T16:00:00.113280+00:00',
            scope: {model: {id: null, display_name: 'Fable'}, surface: null},
        },
    ],
};

let failures = 0;
let checks = 0;

function equal(actual, expected, what) {
    checks++;
    if (JSON.stringify(actual) !== JSON.stringify(expected)) {
        failures++;
        printerr(`FAIL ${what}: got ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)}`);
    }
}

// -- parsing
{
    const {weekly, scoped} = parseLimits(SAMPLE);
    equal(weekly.percent, 71, 'weekly percent');
    equal(levelOf(weekly), 'normal', 'weekly level');
    equal(scoped.percent, 76, 'Fable percent');
    equal(scoped.label, 'Fable', 'Fable label');
    equal(levelOf(scoped), 'warning', 'Fable level follows the API warning');
    equal(scoped.resetsAt.toISOString(), '2026-11-21T16:00:00.113Z', 'microsecond timestamp parses');
}
{
    const other = {...SAMPLE.limits[1], scope: {model: {display_name: 'Opus'}}};
    const {scoped} = parseLimits({limits: [other]});
    equal(scoped.percent, null, 'another model does not fill the Fable bar');
    equal(levelOf(scoped), 'none', 'missing limit has level none');
}
{
    const {weekly, scoped} = parseLimits({seven_day: SAMPLE.seven_day, limits: null});
    equal(weekly.percent, 71, 'falls back to seven_day without a limits list');
    equal(weekly.resetsAt !== null, true, 'fallback keeps the reset time');
    equal(scoped.percent, null, 'fallback has no Fable data');
}
{
    const {weekly} = parseLimits({});
    equal(weekly.percent, null, 'empty payload is no data');
    equal(formatPercent(weekly.percent), '–%', 'no data shows a dash');
    equal(parseLimits(null).weekly.percent, null, 'null payload does not throw');
    equal(parseLimits({limits: [null, {kind: 'weekly_scoped', scope: null},
        {kind: 'weekly_scoped', scope: {model: {display_name: null}}}]}).scoped.percent,
    null, 'null entries and null scopes are skipped');
    equal(parseTime('not a date'), null, 'bad timestamp is null');
    equal(parseTime(undefined), null, 'missing timestamp is null');
}

// -- levels
{
    const at = (percent, severity = 'normal') => levelOf({percent, severity});
    equal(at(10), 'normal', '10% normal');
    equal(at(75), 'warning', '75% warning');
    equal(at(40, 'warning'), 'warning', 'API warning at 40%');
    equal(at(90), 'critical', '90% critical');
    equal(at(130), 'critical', 'over 100% critical');
}

// -- bars and text
{
    equal(barFraction(null), 0, 'no data fills nothing');
    equal(barFraction(50), 0.5, 'half');
    equal(barFraction(250), 1, 'clamped above');
    equal(barFraction(-5), 0, 'clamped below');
    equal(formatPercent(71.4), '71%', 'percent is rounded');
}
{
    // Local-time dates, so the expectations hold in any timezone.
    const now = new Date(2026, 10, 20, 19, 0, 0);                   // Fri 19:00
    const plus = minutes => new Date(now.getTime() + minutes * 60000);
    equal(formatReset(plus(30 * 60), now), 'Resets Sun 01:00 · in 1d 6h', 'days and hours');
    equal(formatReset(plus(5 * 60 + 12), now), 'Resets Sat 00:12 · in 5h 12m', 'hours and minutes');
    equal(formatReset(plus(7), now), 'Resets Fri 19:07 · in 7m', 'minutes');
    equal(formatReset(new Date(plus(30 * 60).getTime() - 100), now),
        'Resets Sun 01:00 · in 1d 6h', 'jitter just before the minute is rounded');
    equal(formatReset(null, now), 'No reset time reported', 'no reset time');
    equal(formatReset(plus(-3), now).startsWith('Reset due'), true, 'past reset');

    equal(formatUpdated(plus(-40), now), 'Updated 18:20', 'updated earlier today');
    equal(formatUpdated(plus(-20 * 60), now), 'Updated Thu 23:00', 'updated yesterday names the day');
}

print(`${checks - failures}/${checks} checks passed`);
if (failures)
    imports.system.exit(1);
