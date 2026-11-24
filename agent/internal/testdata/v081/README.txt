State files as an installed v0.8.1 agent leaves them (fake values), written
by the v0.8.1 code at c87a481 (status.Write, backlog.Store.Add,
config.SaveState; latest.json as the engine marshals protocol.Latest).
Tests read them to prove an upgraded PC keeps its numbers, backlog and
fetch budget.
