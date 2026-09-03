export namespace chdb {
	
	export class ColumnInfo {
	    name: string;
	    type: string;
	
	    static createFrom(source: any = {}) {
	        return new ColumnInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	    }
	}
	export class Discovery {
	    checkvalueColumns: ColumnInfo[];
	    checkvalueEngine: string;
	    replicated: boolean;
	    dateTimePrecision: string;
	    mvColumns: ColumnInfo[];
	    mvHasMaxTime: boolean;
	    excludeDates: string[];
	    problems: string[];
	    replicationDelay: number;
	
	    static createFrom(source: any = {}) {
	        return new Discovery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checkvalueColumns = this.convertValues(source["checkvalueColumns"], ColumnInfo);
	        this.checkvalueEngine = source["checkvalueEngine"];
	        this.replicated = source["replicated"];
	        this.dateTimePrecision = source["dateTimePrecision"];
	        this.mvColumns = this.convertValues(source["mvColumns"], ColumnInfo);
	        this.mvHasMaxTime = source["mvHasMaxTime"];
	        this.excludeDates = source["excludeDates"];
	        this.problems = source["problems"];
	        this.replicationDelay = source["replicationDelay"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace e2e {
	
	export class PlanSummary {
	    totalCells: number;
	    skipCells: number;
	    firstDue: string;
	    lastDue: string;
	
	    static createFrom(source: any = {}) {
	        return new PlanSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.totalCells = source["totalCells"];
	        this.skipCells = source["skipCells"];
	        this.firstDue = source["firstDue"];
	        this.lastDue = source["lastDue"];
	    }
	}

}

export namespace executor {
	
	export class DayResult {
	    checkpointId: number;
	    date: string;
	    planned: number;
	    inserted: number;
	    readbackOk: boolean;
	    readbackNote: string;
	    duplicates: number;
	    checksum: string;
	
	    static createFrom(source: any = {}) {
	        return new DayResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checkpointId = source["checkpointId"];
	        this.date = source["date"];
	        this.planned = source["planned"];
	        this.inserted = source["inserted"];
	        this.readbackOk = source["readbackOk"];
	        this.readbackNote = source["readbackNote"];
	        this.duplicates = source["duplicates"];
	        this.checksum = source["checksum"];
	    }
	}
	export class RunResult {
	    runId: string;
	    profile: string;
	    profileId: string;
	    startedAt: string;
	    finishedAt: string;
	    dryRun: boolean;
	    days: DayResult[];
	    totalRows: number;
	    canceled: boolean;
	    error?: string;
	    scenario: model.Scenario;
	
	    static createFrom(source: any = {}) {
	        return new RunResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.runId = source["runId"];
	        this.profile = source["profile"];
	        this.profileId = source["profileId"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.dryRun = source["dryRun"];
	        this.days = this.convertValues(source["days"], DayResult);
	        this.totalRows = source["totalRows"];
	        this.canceled = source["canceled"];
	        this.error = source["error"];
	        this.scenario = this.convertValues(source["scenario"], model.Scenario);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class AppInfo {
	    name: string;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	    }
	}
	export class CheckpointIDs {
	    ids: number[];
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new CheckpointIDs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ids = source["ids"];
	        this.total = source["total"];
	    }
	}
	export class CheckpointList {
	    items: mariadb.Checkpoint[];
	    columns: string[];
	    total: number;
	    page: number;
	    pageSize: number;
	    hasFlag: boolean;
	    hasMonitor: boolean;
	    hasEnabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CheckpointList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], mariadb.Checkpoint);
	        this.columns = source["columns"];
	        this.total = source["total"];
	        this.page = source["page"];
	        this.pageSize = source["pageSize"];
	        this.hasFlag = source["hasFlag"];
	        this.hasMonitor = source["hasMonitor"];
	        this.hasEnabled = source["hasEnabled"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HistoryList {
	    items: executor.RunResult[];
	    scope: string;
	    hidden: number;
	    sessionName: string;
	
	    static createFrom(source: any = {}) {
	        return new HistoryList(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], executor.RunResult);
	        this.scope = source["scope"];
	        this.hidden = source["hidden"];
	        this.sessionName = source["sessionName"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PendingState {
	    exists: boolean;
	    startedAt?: string;
	    aborted?: string;
	    doneCells: number;
	    noCells: boolean;
	    totalCells: number;
	    profile?: string;
	    target?: string;
	    scenarioJson?: string;
	
	    static createFrom(source: any = {}) {
	        return new PendingState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exists = source["exists"];
	        this.startedAt = source["startedAt"];
	        this.aborted = source["aborted"];
	        this.doneCells = source["doneCells"];
	        this.noCells = source["noCells"];
	        this.totalCells = source["totalCells"];
	        this.profile = source["profile"];
	        this.target = source["target"];
	        this.scenarioJson = source["scenarioJson"];
	    }
	}
	export class PreflightResult {
	    mariaOk: boolean;
	    mariaMsg: string;
	    chOk: boolean;
	    chMsg: string;
	    testOnly: boolean;
	    today: boolean;
	    plan?: e2e.PlanSummary;
	    warnings: string[];
	    fatal: string[];
	
	    static createFrom(source: any = {}) {
	        return new PreflightResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mariaOk = source["mariaOk"];
	        this.mariaMsg = source["mariaMsg"];
	        this.chOk = source["chOk"];
	        this.chMsg = source["chMsg"];
	        this.testOnly = source["testOnly"];
	        this.today = source["today"];
	        this.plan = this.convertValues(source["plan"], e2e.PlanSummary);
	        this.warnings = source["warnings"];
	        this.fatal = source["fatal"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ReportOut {
	    markdown: string;
	    json: string;
	    csv: string;
	
	    static createFrom(source: any = {}) {
	        return new ReportOut(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.markdown = source["markdown"];
	        this.json = source["json"];
	        this.csv = source["csv"];
	    }
	}
	export class ScenarioFile {
	    scenario: model.Scenario;
	    path: string;
	    warnings: string[];
	    problem?: string;
	
	    static createFrom(source: any = {}) {
	        return new ScenarioFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scenario = this.convertValues(source["scenario"], model.Scenario);
	        this.path = source["path"];
	        this.warnings = source["warnings"];
	        this.problem = source["problem"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TestResult {
	    ok: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new TestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.message = source["message"];
	    }
	}

}

export namespace mariadb {
	
	export class Checkpoint {
	    id: number;
	    name: string;
	    driverCode: string;
	    enabled: string;
	    flag: string;
	    enableMonitor: string;
	
	    static createFrom(source: any = {}) {
	        return new Checkpoint(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.driverCode = source["driverCode"];
	        this.enabled = source["enabled"];
	        this.flag = source["flag"];
	        this.enableMonitor = source["enableMonitor"];
	    }
	}

}

export namespace model {
	
	export class HourExpected {
	    date: string;
	    hour: number;
	    dailyCol: string;
	    hourlyCol: string;
	    stats: Stats;
	    statsAll: Stats;
	
	    static createFrom(source: any = {}) {
	        return new HourExpected(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.date = source["date"];
	        this.hour = source["hour"];
	        this.dailyCol = source["dailyCol"];
	        this.hourlyCol = source["hourlyCol"];
	        this.stats = this.convertValues(source["stats"], Stats);
	        this.statsAll = this.convertValues(source["statsAll"], Stats);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Stats {
	    count: number;
	    min: number;
	    max: number;
	    avg: number;
	    sum: number;
	    maxTime?: string;
	    nanCount?: number;
	
	    static createFrom(source: any = {}) {
	        return new Stats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.count = source["count"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.avg = source["avg"];
	        this.sum = source["sum"];
	        this.maxTime = source["maxTime"];
	        this.nanCount = source["nanCount"];
	    }
	}
	export class DayExpected {
	    checkpointId: number;
	    date: string;
	    stats: Stats;
	    statsAll: Stats;
	    hours: HourExpected[];
	    rowCount: number;
	
	    static createFrom(source: any = {}) {
	        return new DayExpected(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.checkpointId = source["checkpointId"];
	        this.date = source["date"];
	        this.stats = this.convertValues(source["stats"], Stats);
	        this.statsAll = this.convertValues(source["statsAll"], Stats);
	        this.hours = this.convertValues(source["hours"], HourExpected);
	        this.rowCount = source["rowCount"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Goal {
	    min: number;
	    max: number;
	    avg: number;
	
	    static createFrom(source: any = {}) {
	        return new Goal(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.min = source["min"];
	        this.max = source["max"];
	        this.avg = source["avg"];
	    }
	}
	
	export class HourOverride {
	    hour: number;
	    mode: string;
	    goal: Goal;
	    nanCount?: number;
	
	    static createFrom(source: any = {}) {
	        return new HourOverride(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hour = source["hour"];
	        this.mode = source["mode"];
	        this.goal = this.convertValues(source["goal"], Goal);
	        this.nanCount = source["nanCount"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Row {
	    log_date: string;
	    checkpoint_id: number;
	    raw_data: number;
	    data: string;
	
	    static createFrom(source: any = {}) {
	        return new Row(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.log_date = source["log_date"];
	        this.checkpoint_id = source["checkpoint_id"];
	        this.raw_data = source["raw_data"];
	        this.data = source["data"];
	    }
	}
	export class Scenario {
	    name: string;
	    checkpointIds: number[];
	    startDate: string;
	    endDate: string;
	    timezone: string;
	    intervalSec: number;
	    daily: Goal;
	    hourlyOverrides: HourOverride[];
	    seed: number;
	    batchSize: number;
	    driverCode: string;
	
	    static createFrom(source: any = {}) {
	        return new Scenario(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.checkpointIds = source["checkpointIds"];
	        this.startDate = source["startDate"];
	        this.endDate = source["endDate"];
	        this.timezone = source["timezone"];
	        this.intervalSec = source["intervalSec"];
	        this.daily = this.convertValues(source["daily"], Goal);
	        this.hourlyOverrides = this.convertValues(source["hourlyOverrides"], HourOverride);
	        this.seed = source["seed"];
	        this.batchSize = source["batchSize"];
	        this.driverCode = source["driverCode"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Preview {
	    scenario: Scenario;
	    days: DayExpected[];
	    totalRows: number;
	    warnings: string[];
	    sampleRows: Row[];
	
	    static createFrom(source: any = {}) {
	        return new Preview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scenario = this.convertValues(source["scenario"], Scenario);
	        this.days = this.convertValues(source["days"], DayExpected);
	        this.totalRows = source["totalRows"];
	        this.warnings = source["warnings"];
	        this.sampleRows = this.convertValues(source["sampleRows"], Row);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	

}

export namespace profile {
	
	export class ClickHouse {
	    host: string;
	    port: number;
	    database: string;
	    user: string;
	    password: string;
	
	    static createFrom(source: any = {}) {
	        return new ClickHouse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.port = source["port"];
	        this.database = source["database"];
	        this.user = source["user"];
	        this.password = source["password"];
	    }
	}
	export class ImportSummary {
	    added: string[];
	    updated: string[];
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new ImportSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.added = source["added"];
	        this.updated = source["updated"];
	        this.warnings = source["warnings"];
	    }
	}
	export class MariaDB {
	    host: string;
	    port: number;
	    database: string;
	    user: string;
	    password: string;
	
	    static createFrom(source: any = {}) {
	        return new MariaDB(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.port = source["port"];
	        this.database = source["database"];
	        this.user = source["user"];
	        this.password = source["password"];
	    }
	}
	export class Profile {
	    id: string;
	    name: string;
	    mariadb: MariaDB;
	    clickhouse: ClickHouse;
	    timezone: string;
	    testOnly: boolean;
	    checkvalueTable: string;
	    dailyStatsChTable: string;
	    dailyStatsTable: string;
	    hourlyTable: string;
	    checkpointTable: string;
	    excludeDateTable: string;
	    createdAt?: string;
	    updatedAt?: string;
	    lastUsedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new Profile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mariadb = this.convertValues(source["mariadb"], MariaDB);
	        this.clickhouse = this.convertValues(source["clickhouse"], ClickHouse);
	        this.timezone = source["timezone"];
	        this.testOnly = source["testOnly"];
	        this.checkvalueTable = source["checkvalueTable"];
	        this.dailyStatsChTable = source["dailyStatsChTable"];
	        this.dailyStatsTable = source["dailyStatsTable"];
	        this.hourlyTable = source["hourlyTable"];
	        this.checkpointTable = source["checkpointTable"];
	        this.excludeDateTable = source["excludeDateTable"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	        this.lastUsedAt = source["lastUsedAt"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace verify {
	
	export class Mismatch {
	    layer: string;
	    cp: number;
	    date: string;
	    hour: number;
	    field: string;
	    expected: string;
	    actual: string;
	    diff: number;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new Mismatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.layer = source["layer"];
	        this.cp = source["cp"];
	        this.date = source["date"];
	        this.hour = source["hour"];
	        this.field = source["field"];
	        this.expected = source["expected"];
	        this.actual = source["actual"];
	        this.diff = source["diff"];
	        this.note = source["note"];
	    }
	}
	export class LayerResult {
	    name: string;
	    ran: boolean;
	    pass: boolean;
	    errored?: boolean;
	    err?: string;
	    checked: number;
	    skipped: number;
	    mismatches: Mismatch[];
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new LayerResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.ran = source["ran"];
	        this.pass = source["pass"];
	        this.errored = source["errored"];
	        this.err = source["err"];
	        this.checked = source["checked"];
	        this.skipped = source["skipped"];
	        this.mismatches = this.convertValues(source["mismatches"], Mismatch);
	        this.note = source["note"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class PathDiff {
	    cp: number;
	    date: string;
	    hour: number;
	    field: string;
	    nanRows: number;
	    finite: string;
	    all: string;
	    actual: string;
	    matched: string;
	    layer: string;
	
	    static createFrom(source: any = {}) {
	        return new PathDiff(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cp = source["cp"];
	        this.date = source["date"];
	        this.hour = source["hour"];
	        this.field = source["field"];
	        this.nanRows = source["nanRows"];
	        this.finite = source["finite"];
	        this.all = source["all"];
	        this.actual = source["actual"];
	        this.matched = source["matched"];
	        this.layer = source["layer"];
	    }
	}
	export class Result {
	    replicationDelay: number;
	    guardPassed: boolean;
	    excludeDates: string[];
	    l1Raw: LayerResult;
	    l1Mv: LayerResult;
	    l2Daily: LayerResult;
	    l2Hourly: LayerResult;
	    warnings: string[];
	    pathDiffs?: PathDiff[];
	    pass: boolean;
	    inconclusive: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.replicationDelay = source["replicationDelay"];
	        this.guardPassed = source["guardPassed"];
	        this.excludeDates = source["excludeDates"];
	        this.l1Raw = this.convertValues(source["l1Raw"], LayerResult);
	        this.l1Mv = this.convertValues(source["l1Mv"], LayerResult);
	        this.l2Daily = this.convertValues(source["l2Daily"], LayerResult);
	        this.l2Hourly = this.convertValues(source["l2Hourly"], LayerResult);
	        this.warnings = source["warnings"];
	        this.pathDiffs = this.convertValues(source["pathDiffs"], PathDiff);
	        this.pass = source["pass"];
	        this.inconclusive = source["inconclusive"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

