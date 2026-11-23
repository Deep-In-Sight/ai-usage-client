// Data and text helpers for the Claude usage indicator.
// No gi imports here, so tests/test_usage.js can exercise this with plain gjs.

export const SCOPED_MODEL = 'Fable';    // which per-model weekly limit gets the second bar

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

export function parseTime(value) {
    if (typeof value !== 'string')
        return null;
    // The API sends microseconds; JS dates hold milliseconds at most.
    const date = new Date(value.replace(/(\.\d{3})\d+/, '$1'));
    return Number.isNaN(date.getTime()) ? null : date;
}

function emptyLimit(label) {
    return {label, percent: null, resetsAt: null, severity: 'normal'};
}

/** Pick the all-models weekly limit and the weekly limit scoped to `model`. */
export function parseLimits(payload, model = SCOPED_MODEL) {
    const weekly = emptyLimit('All models');
    const scoped = emptyLimit(model);
    for (const item of payload?.limits ?? []) {
        let target;
        const scopeModel = item?.scope?.model?.display_name ?? '';
        if (item?.kind === 'weekly_all')
            target = weekly;
        else if (item?.kind === 'weekly_scoped' && scopeModel.toLowerCase() === model.toLowerCase())
            target = scoped;
        else
            continue;
        target.percent = typeof item.percent === 'number' ? item.percent : null;
        target.resetsAt = parseTime(item.resets_at);
        target.severity = item.severity ?? 'normal';
    }
    if (weekly.percent === null) {      // older reply shape without `limits`
        const sevenDay = payload?.seven_day;
        if (typeof sevenDay?.utilization === 'number') {
            weekly.percent = sevenDay.utilization;
            weekly.resetsAt = parseTime(sevenDay.resets_at);
        }
    }
    return {weekly, scoped};
}

/** 'none' | 'normal' | 'warning' | 'critical' */
export function levelOf(limit) {
    if (limit.percent === null)
        return 'none';
    if (limit.percent >= 90)
        return 'critical';
    // The API flags a limit (e.g. "warning") when it is being used up fast,
    // which can be well below 75%.
    if (limit.percent >= 75 || limit.severity !== 'normal')
        return 'warning';
    return 'normal';
}

export function formatPercent(percent) {
    return percent === null ? '–%' : `${Math.round(percent)}%`;
}

/** Fraction of the bar to fill, 0..1. */
export function barFraction(percent) {
    if (percent === null)
        return 0;
    return Math.min(Math.max(percent, 0), 100) / 100;
}

export function formatClock(date) {
    const pad = n => String(n).padStart(2, '0');
    return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** When the shown numbers were fetched; names the day once it is not today. */
export function formatUpdated(date, now = new Date()) {
    if (date.toDateString() === now.toDateString())
        return `Updated ${formatClock(date)}`;
    return `Updated ${DAYS[date.getDay()]} ${formatClock(date)}`;
}

export function formatReset(resetsAt, now = new Date()) {
    if (!resetsAt)
        return 'No reset time reported';
    // The API's reset timestamps jitter around the minute (15:59:59.9 one
    // call, 16:00:00.1 the next), so round to the nearest minute.
    const reset = new Date(Math.round(resetsAt.getTime() / 60000) * 60000);
    const when = `${DAYS[reset.getDay()]} ${formatClock(reset)}`;
    const minutes = Math.floor((reset.getTime() - now.getTime()) / 60000);
    if (minutes <= 0)
        return `Reset due ${when}`;
    const days = Math.floor(minutes / 1440);
    const hours = Math.floor((minutes % 1440) / 60);
    const mins = minutes % 60;
    let left;
    if (days)
        left = `${days}d ${hours}h`;
    else if (hours)
        left = `${hours}h ${mins}m`;
    else
        left = `${mins}m`;
    return `Resets ${when} · in ${left}`;
}
