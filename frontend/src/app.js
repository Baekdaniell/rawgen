// rawgen frontend — Wails 바인딩(window.go.main.App) 호출 담당.
// 계산/DB 로직은 전부 Go 코어에 있고 여기는 표시만 한다.
const $ = (id) => document.getElementById(id);
const api = () => window.go?.main?.App;
const rt = () => window.runtime;

const SCENARIO_STORE_KEY = "rawgen.lastScenario";
const SCENARIO_STORE_KEY_LEGACY = "sg.lastScenario"; // 개명 전 키 (읽기 전용 폴백)
// 세션별 작업 내용(시나리오 = 대상 cp·날짜·목표값·오버라이드) 저장 키.
// 전역 키(lastScenario)는 "마지막 작업" 폴백으로 유지 — 세션 저장분이 없는
// 세션(구버전 데이터·새 세션)은 지금 화면 내용을 물려받은 뒤 자기 키에 저장된다.
const scenarioKeyFor = (id) => "rawgen.scenario." + id;

// 1회성 정리: 세션별 저장 첫 배포판(2026-08-21 오전)은 저장분 없는 세션에 "현재 화면을
// 복사 후 저장"했다 — 세션들을 오가기만 해도 같은 시나리오가 전 세션에 찍혔다.
// 전역 lastScenario와 내용이 같은 세션 키는 그 복사 스탬프이므로 지운다(마지막 사용 세션 제외
// — 전역 키는 항상 활성 세션의 내용과 같아서, 그 세션 것만은 진짜 작업이다).
(function cleanupCopiedScenarioStamps() {
  try {
    if (localStorage.getItem("rawgen.scnCleanup") === "1") return;
    const global = localStorage.getItem(SCENARIO_STORE_KEY);
    const keep = scenarioKeyFor(localStorage.getItem("rawgen.lastSessionId") || "");
    if (global) {
      for (let i = localStorage.length - 1; i >= 0; i--) {
        const k = localStorage.key(i);
        if (k && k.startsWith("rawgen.scenario.") && k !== keep && localStorage.getItem(k) === global) {
          localStorage.removeItem(k);
        }
      }
    }
    localStorage.setItem("rawgen.scnCleanup", "1");
  } catch {}
})();

const state = {
  profiles: [],
  profileId: "",
  // 세션별 마지막 연결 확인 결과 { [id]: {maria: bool|null, ch: bool|null} }.
  // 세션이 여러 개면 "어느 것이 지금 살아 있는 서버인지"를 목록에서 바로 봐야 한다.
  connCheck: {},
  checkpoints: [],
  selectedCps: new Set(),
  scenarioName: "gui",
  overrides: [
    { hour: 2, mode: "null", goal: { min: 0, max: 0, avg: 0 } },
    { hour: 15, mode: "goal", goal: { min: 28, max: 40, avg: 34 } },
  ],
  preview: null,
  running: false, // Generate 실행 중
  historyLoaded: false,
  lastRunOk: false,
  verifyDone: false,
  reportDone: false,
};

const e2eState = {
  running: false,
  hasResult: false,
  filter: null, // null | 상태값 | "problem"
  cells: [],
  nextAtRFC: null,
  estEndRFC: null,
  timer: null,
  pendingNote: null, // 미완료 저장 상태 안내(재개 배너와 동기) — 세션 전환 리셋이 못 지우게 보관
};

const sectionMeta = {
  connections: ["연결", "접속 세션을 등록하고 테스트합니다."],
  targets: ["대상 선택", "데이터를 주입할 checkpoint를 고릅니다."],
  scenario: ["시나리오", "날짜, 주기, 목표 통계값을 입력합니다."],
  preview: ["미리보기", "생성될 데이터와 기대 통계를 확인합니다 (DB 미접근)."],
  generate: ["주입", "ClickHouse에 batch INSERT + readback을 실행합니다."],
  regen: ["제품 재생성", "제품 통계 재생성 유도 절차를 안내합니다."],
  verify: ["검증", "L1(주입 직후) / L2(재생성 후) 검증을 실행합니다."],
  e2e: ["온종일 자동 검증", "주입→L1→시간대별 자동 verify→리포트를 한 번에 수행합니다."],
  report: ["리포트", "결과를 Markdown/JSON/CSV로 정리합니다."],
};

function setSection(name) {
  document.querySelectorAll(".section").forEach((el) => el.classList.toggle("active", el.id === name));
  document.querySelectorAll(".step").forEach((el) => el.classList.toggle("active", el.dataset.section === name));
  const [t, s] = sectionMeta[name];
  $("sectionTitle").textContent = t;
  $("sectionSubtitle").textContent = s;
  // 섹션 진입 훅: 자동 로드로 클릭 왕복을 줄인다
  if (name === "generate" && !state.historyLoaded && api()) loadHistory().catch(() => {});
  if (name === "report" && !lastReport && api()) buildReport().catch(() => {});
}

function showError(err) {
  const msg = err instanceof Error ? err.message : String(err);
  $("errorText").textContent = `[${$("sectionTitle").textContent}] ${msg}`;
  $("errorStrip").style.display = "";
}
function clearError() {
  $("errorStrip").style.display = "none";
}
function esc(s) {
  return String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}
function fmt(n, d = 6) {
  // 백엔드는 NaN/Inf를 JSON 숫자로 보낼 수 없어 문자열로 보낸다(model/jsonfloat.go).
  if (n === "NaN" || n === "Inf" || n === "-Inf") return n;
  if (n === null || n === undefined || Number.isNaN(n)) return "NULL";
  return Number(n).toLocaleString("en-US", { maximumFractionDigits: d });
}
function guard(fn) {
  return async (...args) => {
    try {
      clearError();
      if (!api()) throw new Error("Wails 바인딩이 없습니다. 데스크톱 앱으로 실행하세요.");
      await fn(...args);
    } catch (err) {
      showError(err);
    }
  };
}

// 비동기 액션 중 버튼을 잠그고 라벨을 바꿔 "멈춘 것처럼 보이는" 상태와
// 재클릭으로 인한 IPC 이중 호출을 막는다 (verify는 복제 지연 가드로 1분+ 걸릴 수 있음)
function bindBusy(id, fn, busyLabel) {
  const btn = $(id);
  btn.addEventListener(
    "click",
    guard(async () => {
      const orig = btn.textContent;
      btn.disabled = true;
      if (busyLabel) btn.textContent = busyLabel;
      try {
        await fn(btn);
      } finally {
        btn.disabled = false;
        btn.textContent = orig;
      }
    }),
  );
}

function flashButton(btn, label) {
  const orig = btn.textContent;
  btn.textContent = label;
  setTimeout(() => (btn.textContent = orig), 1500);
}

function hm(d) {
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}
function dur(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h) return `${h}시간 ${m}분`;
  if (m) return `${m}분 ${sec}초`;
  return `${sec}초`;
}

// ---------- 공용 테이블 헬퍼 ----------

// thead th 클릭으로 tbody 정렬 (숫자면 수치 비교). hint 단독 행은 건너뜀.
function makeSortable(table) {
  const ths = table.querySelectorAll("thead th");
  ths.forEach((th, idx) => {
    if (!th.textContent.trim()) return;
    th.classList.add("sortable");
    th.addEventListener("click", () => {
      const tbody = table.querySelector("tbody");
      const rows = [...tbody.querySelectorAll("tr")].filter((r) => r.children.length > 1);
      if (rows.length < 2) return;
      const asc = !th.classList.contains("sort-asc");
      ths.forEach((t) => t.classList.remove("sort-asc", "sort-desc"));
      th.classList.add(asc ? "sort-asc" : "sort-desc");
      rows.sort((a, b) => {
        const av = a.children[idx]?.textContent.trim() ?? "";
        const bv = b.children[idx]?.textContent.trim() ?? "";
        const an = parseFloat(av.replace(/,/g, ""));
        const bn = parseFloat(bv.replace(/,/g, ""));
        const cmp = !Number.isNaN(an) && !Number.isNaN(bn) ? an - bn : av.localeCompare(bv);
        return asc ? cmp : -cmp;
      });
      rows.forEach((r) => tbody.appendChild(r));
    });
  });
}

// 표를 탭 구분 텍스트로 복사 — Jira/Excel 붙여넣기용 (숨김 행 제외)
async function copyTableTSV(table, btn) {
  const lines = [];
  const head = [...table.querySelectorAll("thead th")].map((th) => th.textContent.trim());
  lines.push(head.join("\t"));
  for (const tr of table.querySelectorAll("tbody tr")) {
    if (tr.style.display === "none" || tr.children.length < 2) continue;
    lines.push([...tr.children].map((td) => td.textContent.trim()).join("\t"));
  }
  await navigator.clipboard.writeText(lines.join("\n"));
  if (btn) flashButton(btn, "복사됨");
}

// ---------- 사이드바 단계 상태 ----------

function updateStepStates() {
  const flags = {
    connections: { done: !!state.profileId },
    targets: { done: state.selectedCps.size > 0 },
    scenario: { done: !!($("startDate").value && $("endDate").value) },
    preview: { done: !!state.preview, blocked: state.selectedCps.size === 0 },
    generate: { done: state.lastRunOk, blocked: !state.preview },
    regen: { done: !!$("checklistText").value },
    verify: { done: state.verifyDone },
    e2e: { done: e2eState.hasResult },
    report: { done: state.reportDone },
  };
  document.querySelectorAll(".step").forEach((el) => {
    const f = flags[el.dataset.section] || {};
    el.classList.toggle("done", !!f.done);
    el.classList.toggle("blocked", !!f.blocked && !f.done);
  });
}

// ---------- Connections ----------

function profileFromForm() {
  return {
    id: state.profileId,
    name: $("pfName").value.trim(),
    testOnly: $("pfTestOnly").checked,
    timezone: $("pfTimezone").value.trim() || "Asia/Seoul",
    mariadb: {
      host: $("pfMariaHost").value.trim(),
      port: Number($("pfMariaPort").value) || 3306,
      database: $("pfMariaDb").value.trim() || "liz",
      user: $("pfMariaUser").value.trim(),
      password: $("pfMariaPw").value,
    },
    clickhouse: {
      host: $("pfChHost").value.trim(),
      port: Number($("pfChPort").value) || 9000,
      database: $("pfChDb").value.trim() || "liz",
      user: $("pfChUser").value.trim() || "default",
      password: $("pfChPw").value,
    },
    // 빈 값 = "기본 테이블명 사용". 백엔드가 빈 값을 기존 설정으로 머지하므로
    // 여기서 하드코딩 ""를 보내면 배포본 오버라이드가 조용히 지워진다.
    checkvalueTable: $("pfTblCheckvalue").value.trim(),
    dailyStatsChTable: $("pfTblDailyCh").value.trim(),
    dailyStatsTable: $("pfTblDaily").value.trim(),
    hourlyTable: $("pfTblHourly").value.trim(),
    checkpointTable: $("pfTblCheckpoint").value.trim(),
    excludeDateTable: $("pfTblExcludeDate").value.trim(),
  };
}

function fillForm(p) {
  state.profileId = p?.id || "";
  $("pfName").value = p?.name || "";
  $("pfTestOnly").checked = !!p?.testOnly;
  $("pfTimezone").value = p?.timezone || "Asia/Seoul";
  $("pfMariaHost").value = p?.mariadb?.host || "";
  $("pfMariaPort").value = p?.mariadb?.port || 3306;
  $("pfMariaDb").value = p?.mariadb?.database || "liz";
  $("pfMariaUser").value = p?.mariadb?.user || "";
  $("pfMariaPw").value = p?.mariadb?.password || "";
  $("pfChHost").value = p?.clickhouse?.host || "";
  $("pfChPort").value = p?.clickhouse?.port || 9000;
  $("pfChDb").value = p?.clickhouse?.database || "liz";
  $("pfChUser").value = p?.clickhouse?.user || "default";
  $("pfChPw").value = p?.clickhouse?.password || "";
  $("pfTblCheckvalue").value = p?.checkvalueTable || "";
  $("pfTblDailyCh").value = p?.dailyStatsChTable || "";
  $("pfTblDaily").value = p?.dailyStatsTable || "";
  $("pfTblHourly").value = p?.hourlyTable || "";
  $("pfTblCheckpoint").value = p?.checkpointTable || "";
  $("pfTblExcludeDate").value = p?.excludeDateTable || "";
  // 데이터 소스 목록: host가 있으면 "추가된" 소스. 선택은 유효하면 유지한다
  // (연결 테스트가 저장→재로드를 부르는데, 그때 열어둔 폼이 닫히면 안 된다).
  dsState.added.maria = !!p?.mariadb?.host;
  dsState.added.ch = !!p?.clickhouse?.host;
  if (!dsState.selected || !dsState.added[dsState.selected]) {
    dsState.selected = dsState.added.maria ? "maria" : dsState.added.ch ? "ch" : null;
  }
  renderDataSources();
  renderSideProfile();
}

// ---------- 데이터 소스 목록 (DataGrip "데이터 소스 추가" 문법) ----------
// 백엔드 스키마는 세션당 MariaDB+ClickHouse 각 1개로 고정 — 목록/추가 메뉴는 그 두
// 슬롯의 표면이다. 입력 필드 id는 그대로라 profileFromForm/fillForm은 무변경.

const DS_META = {
  maria: { name: "MariaDB", icon: "ds-icon-maria", form: "dsFormMaria", connKey: "maria", hostId: "pfMariaHost", portId: "pfMariaPort", dbId: "pfMariaDb" },
  ch: { name: "ClickHouse", icon: "ds-icon-ch", form: "dsFormCh", connKey: "ch", hostId: "pfChHost", portId: "pfChPort", dbId: "pfChDb" },
};
const dsState = { added: { maria: false, ch: false }, selected: null };

function renderDataSources() {
  const rows = [];
  for (const k of ["maria", "ch"]) {
    const m = DS_META[k];
    $(m.form).hidden = !(dsState.added[k] && dsState.selected === k);
    const item = document.querySelector(`.ds-menu-item[data-ds="${k}"]`);
    item.disabled = dsState.added[k];
    if (!dsState.added[k]) continue;
    const host = $(m.hostId).value.trim();
    const sub = host ? `${host}:${$(m.portId).value} / ${$(m.dbId).value}` : "호스트 미입력";
    const v = state.connCheck[state.profileId]?.[m.connKey];
    rows.push(`<div class="ds-row${dsState.selected === k ? " active" : ""}" data-ds="${k}">
      <span class="ds-icon ${m.icon}" aria-hidden="true"></span>
      <span style="min-width:0"><span class="ds-row-name">${m.name}</span><br /><span class="ds-row-sub">${esc(sub)}</span></span>
      <span class="sx-dot ${v === true ? "ok" : v === false ? "bad" : ""}" title="${v === true ? "연결 확인됨" : v === false ? "연결 실패" : "미확인"}"></span>
    </div>`);
  }
  $("dsList").innerHTML = rows.length
    ? rows.join("")
    : '<p class="hint">추가된 데이터 소스가 없습니다 — [+ 데이터 소스 추가]로 시작하세요.</p>';
}

function closeDsMenu() {
  $("dsAddMenu").hidden = true;
  $("dsAddBtn").setAttribute("aria-expanded", "false");
}

// 제거는 두 번 클릭(입력해 둔 호스트·비밀번호가 날아간다) — 네이티브 confirm은
// WebView를 멈추므로 resumeDiscard와 같은 인라인 무장 패턴을 쓴다.
function armThenRun(btn, fn) {
  if (btn.dataset.armed) {
    delete btn.dataset.armed;
    btn.textContent = "×";
    fn();
    return;
  }
  btn.dataset.armed = "1";
  btn.textContent = "제거 확인";
  setTimeout(() => {
    if (btn.dataset.armed) {
      delete btn.dataset.armed;
      btn.textContent = "×";
    }
  }, 4000);
}

// 소스 제거 = 해당 슬롯을 기본값으로 비운다(저장 전까지는 로컬 변경일 뿐).
const DS_DEFAULTS = {
  maria: () => {
    $("pfMariaHost").value = "";
    $("pfMariaPort").value = 3306;
    $("pfMariaDb").value = "liz";
    $("pfMariaUser").value = "";
    $("pfMariaPw").value = "";
  },
  ch: () => {
    $("pfChHost").value = "";
    $("pfChPort").value = 9000;
    $("pfChDb").value = "liz";
    $("pfChUser").value = "default";
    $("pfChPw").value = "";
  },
};

function removeDataSource(k) {
  DS_DEFAULTS[k]();
  dsState.added[k] = false;
  if (dsState.selected === k) {
    const other = k === "maria" ? "ch" : "maria";
    dsState.selected = dsState.added[other] ? other : null;
  }
  renderDataSources();
}

function currentProfile() {
  return state.profiles.find((p) => p.id === state.profileId);
}

function renderSideProfile() {
  const p = currentProfile();
  // 하단 상태 바 — 대상 세션·INSERT 허용 여부를 화면 최하단에 상시 노출(IDE 문법)
  $("stSession").textContent = p ? `${p.name} @ ${p.clickhouse?.host || "?"}` : "세션 없음";
  const st = $("stTestOnly");
  st.textContent = p ? (p.testOnly ? "INSERT 허용" : "INSERT 차단") : "";
  st.className = "sb-item " + (p?.testOnly ? "sb-warn" : "sb-ok");
  updateInsertButton();
  updateE2EButtons();
  updateStepStates();
}

async function loadProfiles(selectId) {
  state.profiles = (await api().ListProfiles()) || [];
  // 재시작 시에도 마지막으로 쓰던 세션이 선택돼야 그 작업 내용에서 이어진다
  let last = "";
  try {
    last = localStorage.getItem("rawgen.lastSessionId") || "";
  } catch {}
  const wanted = selectId || state.profileId || last;
  const chosen = state.profiles.find((p) => p.id === wanted) || state.profiles[0];
  activateSession(chosen || null);
  renderSessionList();
}

// 실행 중 세션 컨텍스트 변경 차단 — 돌고 있는 주입/E2E의 결과(gen:done·e2e:done)가
// 화면에서 다른 세션 소속으로 표시·집계되는 오귀속을 막는다.
function blockDuringRun(what) {
  if (!state.running && !e2eState.running) return false;
  const code = what.charCodeAt(what.length - 1) - 0xac00;
  const josa = code >= 0 && code < 11172 && code % 28 > 0 ? "은" : "는";
  showError(
    `${what}${josa} 실행 중에는 할 수 없습니다 — ${
      e2eState.running ? "E2E를 중단(지금까지의 판정은 저장됩니다)" : "주입을 완료/취소"
    }한 뒤 다시 시도하세요.`,
  );
  return true;
}

// 세션 전환 시 이전 세션의 실행 결과를 그대로 두면 "다른 서버의 결과를 이 세션 것으로
// 읽는" 오판이 된다(미검증≠통과와 같은 계열). 화면에 남는 결과 표시물 전부가 대상이다:
// 대상 목록·스키마 조회·preview 표·주입 로그·재생성 체크리스트·verify 결과·E2E 결과·리포트.
// (E2E 백엔드 상태 파일은 불변 — 재개 배너가 따로 안내한다)
function resetSessionWork() {
  state.checkpoints = [];
  renderCheckpointTable();
  state.historyLoaded = false;
  state.lastRunOk = false;
  state.verifyDone = false;
  state.reportDone = false;
  lastReport = null;
  // 연결: 이전 세션의 스키마 조회·가져오기 결과
  $("discoverOut").textContent = "세션 저장 후 실행하세요.";
  $("importResult").textContent = "";
  // 미리보기: 게이트만 닫으면 표·지표가 이전 세션 숫자로 남는다 — 표시물까지 비운다
  state.preview = null;
  for (const id of ["metricRows", "metricDays", "metricWarnings", "metricBatches"]) $(id).textContent = "-";
  $("previewWarnings").style.display = "none";
  $("dailyExpected").innerHTML = "";
  $("hourlyDaySelect").innerHTML = "";
  $("hourlyPreview").innerHTML = "";
  $("sampleRows").textContent = "Preview를 실행하세요.";
  // 주입: 확인 체크·요약·로그·진행바
  $("genConfirm").checked = false;
  $("genNaNConfirm").checked = false;
  $("genSummary").textContent = "Preview를 먼저 실행하세요.";
  $("genLog").textContent = "";
  setProgress(0, 1);
  // 재생성: 남겨두면 이전 세션의 체크리스트가 이 세션의 "완료"로 읽힌다
  $("checklistText").value = "";
  // 검증
  $("verifyLayers").innerHTML = '<tr><td colspan="7" class="hint">검증을 실행하면 결과가 표시됩니다.</td></tr>';
  $("verifyBanner").textContent = "";
  $("mismatchRows").innerHTML = '<tr><td colspan="8" class="hint">검증을 실행하면 불일치 표본이 표시됩니다.</td></tr>';
  // E2E 결과 표시
  e2eState.hasResult = false;
  e2eState.filter = null;
  renderE2ECells([]);
  $("e2eProblemOnly").classList.remove("active");
  $("e2eStatus").textContent = e2eState.pendingNote || "대기";
  $("e2eLog").textContent = "";
  $("e2ePreflightPanel").style.display = "none";
  // 리포트
  $("reportText").value = "Preview/Generate/Verify 후 생성하세요.";
  updateInsertButton();
  updateE2EButtons();
  updateStepStates();
}

// 저장된 작업이 없는 세션의 초기 상태 — 부팅 기본값과 같은 빈 시나리오.
// 다른 세션의 내용을 물려받지 않는다: 세션마다 자기 작업대가 따로라는 게 눈에 보여야 하고,
// 다른 서버용으로 짠 날짜·cp가 복사돼 오면 오주입의 씨앗이 된다.
function defaultScenario() {
  // 날짜도 비워 둔다 — 채워 주면 시나리오 단계가 시작부터 "완료"로 보이고(스테퍼 체크),
  // 무엇보다 날짜는 주입 대상의 절반이라 사용자가 직접 고르는 게 맞다.
  return {
    name: "gui",
    checkpointIds: [],
    startDate: "",
    endDate: "",
    intervalSec: 10,
    driverCode: "numeric_percent",
    seed: 325,
    batchSize: 100000,
    daily: { min: 20, max: 40, avg: 30 },
    hourlyOverrides: [
      { hour: 2, mode: "null", goal: { min: 0, max: 0, avg: 0 } },
      { hour: 15, mode: "goal", goal: { min: 28, max: 40, avg: 34 } },
    ],
  };
}

// 세션 선택의 단일 경로 — 접속 폼을 채우고, 실제로 다른 세션이면 작업 결과를 리셋한 뒤
// 그 세션의 저장된 시나리오를 복원한다. 저장분이 없으면 빈 기본 시나리오로 시작한다.
// 예외: 페이지 로드 후 첫 활성화(부팅)만은 화면에 복원돼 있는 전역 lastScenario를 물려받는다
// — 세션별 저장이 없던 시절의 마지막 작업이 "마지막에 쓰던 세션" 소속으로 이관되는 경로.
let bootActivation = true;
function activateSession(p) {
  const prevId = state.profileId;
  const changed = (p?.id || "") !== prevId;
  const isBoot = bootActivation;
  bootActivation = false;
  fillForm(p);
  if (!changed) return;
  resetSessionWork();
  if (!p?.id) return;
  try {
    localStorage.setItem("rawgen.lastSessionId", p.id);
  } catch {}
  let restored = false;
  try {
    const raw = localStorage.getItem(scenarioKeyFor(p.id));
    if (raw) {
      applyScenario(JSON.parse(raw));
      restored = true;
    }
  } catch {}
  if (!restored) {
    // isBoot: 전역 복원분을 이 세션 것으로 이관. prevId "": 새 세션 초안을 저장한
    // 직후라 화면 내용(초안 작업대)이 곧 이 세션의 시나리오다. 그 외 = 빈 기본값.
    if (isBoot || prevId === "") refreshScenarioJson();
    else applyScenario(defaultScenario());
  }
}

// 새 세션 초안 시작 — 접속 폼·작업 결과·시나리오 전부 빈 상태에서 출발한다.
// fillForm(null)만 부르면 접속 폼 밖의 것(대상 선택·시나리오·스테퍼 체크)이
// 이전 세션 것으로 남는다(activateSession 경로가 아니라 리셋을 안 탄다).
function startNewSessionDraft() {
  fillForm(null);
  resetSessionWork();
  applyScenario(defaultScenario());
  renderSessionList();
}

function shortTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// renderSessionList는 좌측 세션 탐색기를 그린다(DataGrip 탐색기 문법).
// 선택 항목이 곧 "지금 작업 대상"이고 주입/검증/E2E가 전부 이 선택을 따라가므로,
// 어느 항목이 선택됐는지가 어느 화면에서든 보여야 한다.
function renderSessionList() {
  const connDot = (id, key) => {
    const v = state.connCheck[id]?.[key];
    const cls = v === true ? "ok" : v === false ? "bad" : "";
    return `<span class="sx-dot ${cls}" title="${key === "ch" ? "ClickHouse" : "MariaDB"} ${
      v === true ? "연결 확인됨" : v === false ? "연결 실패" : "미확인"
    }"></span>`;
  };
  const items = state.profiles
    .map((p) => {
      const active = p.id === state.profileId;
      return `<div class="sx-item${active ? " active" : ""}" data-id="${esc(p.id)}"
        title="마지막 사용 ${esc(shortTime(p.lastUsedAt))}">
        <div class="sx-name">${esc(p.name)}</div>
        <div class="sx-sub">
          <span class="sx-host">${esc(p.clickhouse?.host || "-")}</span>
          ${connDot(p.id, "ch")}${connDot(p.id, "maria")}
          <span class="sx-badge ${p.testOnly ? "sx-warn" : ""}">${p.testOnly ? "INSERT" : "읽기"}</span>
        </div>
      </div>`;
    })
    .join("");
  $("sessionExplorer").innerHTML =
    items || '<p class="hint">등록된 세션이 없습니다 — "새 세션"으로 추가하세요.</p>';
  document.querySelectorAll("#sessionExplorer .sx-item").forEach((el) =>
    el.addEventListener("click", () => {
      const p = state.profiles.find((x) => x.id === el.dataset.id);
      if (!p) return;
      if (p.id !== state.profileId && blockDuringRun("세션 전환")) return;
      activateSession(p);
      renderSessionList();
    }),
  );
}

// ---------- Targets ----------

function renderSelectedCps() {
  // cp 목록 변경도 시나리오 변경이다 — 확인한 Preview의 대상과 달라지면 게이트를 닫는다.
  invalidatePreviewIfChanged();
  const box = $("cpSelected");
  const ids = [...state.selectedCps].sort((a, b) => a - b);
  box.innerHTML = ids.length
    ? ids.map((id) => `<span>${id} <a href="#" data-remove-cp="${id}" style="color:inherit">×</a></span>`).join("")
    : '<span class="hint">선택된 checkpoint가 없습니다.</span>';
  refreshScenarioJson();
  updateStepStates();
}

function renderCheckpointTable() {
  $("cpRows").innerHTML = state.checkpoints.length
    ? state.checkpoints
        .map(
          (cp) => `<tr>
            <td><input type="checkbox" data-cp="${cp.id}" ${state.selectedCps.has(cp.id) ? "checked" : ""} /></td>
            <td>${cp.id}</td><td>${esc(cp.name)}</td><td>${esc(cp.driverCode)}</td><td>${esc(cp.enabled)}</td>
          </tr>`,
        )
        .join("")
    : '<tr><td colspan="5" class="hint">결과 없음</td></tr>';
}

// ---------- Scenario ----------

function toMariaHour(h) {
  return `_${String(h + 1).padStart(2, "0")}`;
}

function renderHourRows() {
  $("hourRows").innerHTML = state.overrides
    .map(
      (row, i) => `<tr>
        <td><input type="number" min="0" max="23" value="${row.hour}" data-oi="${i}" data-of="hour" /></td>
        <td data-maria-cell="${i}">${toMariaHour(Number(row.hour))}</td>
        <td><select data-oi="${i}" data-of="mode">
          <option value="goal" ${row.mode === "goal" ? "selected" : ""}>goal</option>
          <option value="null" ${row.mode === "null" ? "selected" : ""}>NULL</option>
        </select></td>
        <td><input type="number" step="0.001" value="${row.goal.min}" data-oi="${i}" data-of="min" /></td>
        <td><input type="number" step="0.001" value="${row.goal.max}" data-oi="${i}" data-of="max" /></td>
        <td><input type="number" step="0.001" value="${row.goal.avg}" data-oi="${i}" data-of="avg" /></td>
        <td><input type="number" min="0" step="1" value="${row.nanCount || 0}" data-oi="${i}" data-of="nanCount" /></td>
        <td><button class="remove" data-remove-hour="${i}">×</button></td>
      </tr>`,
    )
    .join("");
  refreshScenarioJson();
}

function buildScenario() {
  return {
    name: state.scenarioName || "gui",
    checkpointIds: [...state.selectedCps],
    startDate: $("startDate").value,
    endDate: $("endDate").value,
    timezone: currentProfile()?.timezone || $("pfTimezone").value.trim() || "Asia/Seoul",
    intervalSec: Number($("intervalSec").value),
    daily: {
      min: Number($("dailyMin").value),
      max: Number($("dailyMax").value),
      avg: Number($("dailyAvg").value),
    },
    hourlyOverrides: state.overrides.map((o) => ({
      hour: Number(o.hour),
      mode: o.mode,
      goal: { min: Number(o.goal.min), max: Number(o.goal.max), avg: Number(o.goal.avg) },
      nanCount: Number(o.nanCount) || 0,
    })),
    seed: Number($("seed").value),
    batchSize: Number($("batchSize").value) || 100000,
    driverCode: $("driverCode").value,
  };
}

// invalidatePreview는 시나리오가 바뀌었을 때 주입 게이트를 원위치시킨다.
// preview 존재 여부만 보고 INSERT를 허용하면, 화면에는 옛 대상이 남은 채
// 확인한 적 없는 (날짜×cp)에 실제로 주입된다.
function invalidatePreview() {
  if (!state.preview) return;
  state.preview = null;
  const cb = $("genConfirm");
  if (cb) cb.checked = false;
  const sum = $("genSummary");
  if (sum) sum.textContent = "시나리오가 바뀌었습니다 — Preview를 다시 실행하세요.";
  updateInsertButton();
  updateStepStates();
}

// 폼과 preview의 시나리오가 다르면 무효화한다.
function invalidatePreviewIfChanged() {
  if (!state.preview) return;
  try {
    if (JSON.stringify(state.preview.scenario) !== JSON.stringify(buildScenario())) invalidatePreview();
  } catch {
    invalidatePreview();
  }
}

function refreshScenarioJson() {
  const s = buildScenario();
  $("scenarioJson").textContent = JSON.stringify(s, null, 2);
  invalidatePreviewIfChanged();
  try {
    const raw = JSON.stringify(s);
    localStorage.setItem(SCENARIO_STORE_KEY, raw);
    // 세션이 선택돼 있으면 그 세션의 작업 내용으로도 저장 — 전환 시 이걸 복원한다
    if (state.profileId) localStorage.setItem(scenarioKeyFor(state.profileId), raw);
  } catch {}
  updateStepStates();
}

// select에 없는 값이면 옵션을 추가해서라도 시나리오 값을 보존한다
function setSelectValue(id, v) {
  const sel = $(id);
  if (![...sel.options].some((o) => o.value === v)) {
    const opt = document.createElement("option");
    opt.value = v;
    opt.textContent = v;
    sel.appendChild(opt);
  }
  sel.value = v;
}

// 시나리오 객체를 폼에 역주입 — 파일 불러오기·자동 복원·E2E 재개가 모두 이 경로
function applyScenario(s) {
  state.scenarioName = s.name || "gui";
  state.selectedCps = new Set((s.checkpointIds || []).map(Number));
  // 무조건 대입 — 빈 값을 건너뛰면 이전 세션의 날짜가 새 작업대에 남는다
  $("startDate").value = s.startDate || "";
  $("endDate").value = s.endDate || "";
  setSelectValue("intervalSec", String(s.intervalSec ?? 10));
  setSelectValue("driverCode", s.driverCode || "numeric_percent");
  $("seed").value = s.seed ?? 325;
  $("batchSize").value = s.batchSize || 100000;
  $("dailyMin").value = s.daily?.min ?? 0;
  $("dailyMax").value = s.daily?.max ?? 0;
  $("dailyAvg").value = s.daily?.avg ?? 0;
  state.overrides = (s.hourlyOverrides || []).map((o) => ({
    hour: o.hour,
    mode: o.mode || "goal",
    goal: { min: o.goal?.min ?? 0, max: o.goal?.max ?? 0, avg: o.goal?.avg ?? 0 },
    nanCount: o.nanCount ?? 0,
  }));
  renderHourRows();
  renderSelectedCps();
  if (state.checkpoints.length) renderCheckpointTable();
  invalidatePreviewIfChanged();
}

// ---------- Preview ----------

function renderPreview(pv) {
  state.preview = pv;
  $("metricRows").textContent = fmt(pv.totalRows, 0);
  $("metricDays").textContent = String(pv.days?.length ?? 0);
  $("metricWarnings").textContent = String(pv.warnings?.length ?? 0);
  const bs = pv.scenario.batchSize || 100000;
  $("metricBatches").textContent = String(Math.ceil(pv.totalRows / bs));

  const wb = $("previewWarnings");
  if (pv.warnings?.length) {
    wb.style.display = "";
    wb.innerHTML = pv.warnings.map((w) => `<li>${esc(w)}</li>`).join("");
  } else {
    wb.style.display = "none";
  }

  $("dailyExpected").innerHTML = (pv.days || [])
    .map((d) => {
      const st = d.stats;
      if (!st.count) {
        return `<tr><td>${d.checkpointId}</td><td>${d.date}</td><td>0</td><td>NULL</td><td>NULL</td><td>NULL</td><td>NULL</td></tr>`;
      }
      return `<tr><td>${d.checkpointId}</td><td>${d.date}</td><td>${fmt(st.count, 0)}</td>
        <td>${fmt(st.min, 3)}</td><td>${fmt(st.max, 3)}</td><td>${fmt(st.avg, 6)}</td><td>${esc(st.maxTime || "NULL")}</td></tr>`;
    })
    .join("");

  const sel = $("hourlyDaySelect");
  sel.innerHTML = (pv.days || [])
    .map((d, i) => `<option value="${i}">cp ${d.checkpointId} / ${d.date}</option>`)
    .join("");
  renderHourly(0);

  $("sampleRows").textContent = (pv.sampleRows || []).map((r) => JSON.stringify(r)).join("\n") || "(없음)";

  const p = currentProfile();
  const s = pv.scenario;
  $("genSummary").textContent = [
    `세션: ${p ? p.name : "(미선택)"}  TestOnly=${p ? p.testOnly : "-"}`,
    `ClickHouse: ${p?.clickhouse?.host || "?"}:${p?.clickhouse?.port || "?"} / ${p?.clickhouse?.database || "?"} → ${p?.checkvalueTable || "checkvalue"}`,
    `날짜: ${s.startDate} .. ${s.endDate} (${s.timezone})`,
    `checkpoint ${s.checkpointIds.length}개: ${s.checkpointIds.join(", ")}`,
    `예상 row: ${pv.totalRows.toLocaleString()} / batch ${Math.ceil(pv.totalRows / bs)}개 (size ${bs.toLocaleString()})`,
    `경고 ${pv.warnings?.length || 0}건${pv.warnings?.length ? " — 미리보기 탭에서 확인" : ""}`,
    ``,
    `주입 후 이 (날짜 × cp)는 다른 검증의 클린 대조 기준으로 사용 금지.`,
    ...(scenarioHasNaN() ? [`NaN 주입 포함 — MV 경로와 checkvalue 폴백 경로의 통계가 갈립니다(의도된 경로 차이 검출).`] : []),
  ].join("\n");
  updateInsertButton();
  updateStepStates();
}

function renderHourly(idx) {
  const d = state.preview?.days?.[idx];
  if (!d) {
    $("hourlyPreview").innerHTML = "";
    return;
  }
  $("hourlyPreview").innerHTML = d.hours
    .map((h) => {
      const st = h.stats;
      const all = h.statsAll || st;
      // NaN이 있으면 표시값(폴백 경로 기대)과 MV 경로 기대가 갈린다 — 둘 다 보여준다.
      const nanCell = st.nanCount
        ? `<span class="nan-mark">NaN ${st.nanCount}행</span><br>count ${fmt(all.count, 0)} · avg ${fmt(all.avg, 6)}`
        : "-";
      return `<tr class="${st.nanCount ? "row-nan" : ""}"><td>${h.hour}</td><td>${h.dailyCol}</td><td>${h.hourlyCol}</td>
        <td>${fmt(st.count, 0)}</td><td>${st.count ? fmt(st.min, 3) : "NULL"}</td>
        <td>${st.count ? fmt(st.max, 3) : "NULL"}</td><td>${st.count ? fmt(st.avg, 6) : "NULL"}</td>
        <td>${esc(st.maxTime || "NULL")}</td><td>${nanCell}</td></tr>`;
    })
    .join("");
}

// ---------- Generate ----------

// scenarioHasNaN은 현재 폼 기준으로 NaN 주입 여부를 본다.
function scenarioHasNaN() {
  return state.overrides.some((o) => Number(o.nanCount) > 0 && o.mode !== "null");
}

function updateNaNBox() {
  const has = scenarioHasNaN();
  const box = $("genNaNBox");
  if (!box) return has;
  box.style.display = has ? "" : "none";
  if (!has) $("genNaNConfirm").checked = false;
  return has;
}

function updateInsertButton() {
  const p = currentProfile();
  const anyRun = state.running || e2eState.running;
  // NaN 주입은 일반 확인과 별도의 확인을 요구한다(실수로 켜지지 않게).
  const nanOK = !updateNaNBox() || $("genNaNConfirm").checked;
  const ok = !!state.preview && !!p?.testOnly && $("genConfirm").checked && nanOK && !anyRun;
  $("runInsert").disabled = !ok;
  $("runDry").disabled = anyRun;
  $("cancelRun").disabled = !state.running;
  // E2E 대기 중 같은 (cp×날짜)에 추가 INSERT·동시 verify는 expected/결과를 오염시킴
  $("verifyL1").disabled = e2eState.running;
  $("verifyAll").disabled = e2eState.running;
  const busyText = e2eState.running
    ? "E2E 실행 중 — Generate/Verify 잠김"
    : state.running
      ? "Generate 실행 중"
      : "";
  $("stBusy").textContent = busyText;
}

function genLog(line) {
  const el = $("genLog");
  el.textContent += line + "\n";
  el.scrollTop = el.scrollHeight;
}

function setProgress(done, total) {
  const pct = total > 0 ? Math.min(100, (done / total) * 100) : 0;
  $("progressBar").style.width = pct + "%";
}

async function startRun(dryRun) {
  if (!state.preview) throw new Error("Preview를 먼저 실행하세요.");
  const p = currentProfile();
  if (!p) throw new Error("세션을 선택하세요.");
  // 마지막 방어선: 확인한 시나리오와 실제로 보낼 시나리오가 같은지 대조한다.
  if (JSON.stringify(state.preview.scenario) !== JSON.stringify(buildScenario())) {
    invalidatePreview();
    throw new Error("확인한 Preview와 현재 시나리오가 다릅니다 — Preview를 다시 실행해 대상을 확인하세요.");
  }
  state.running = true;
  updateInsertButton();
  $("genLog").textContent = "";
  setProgress(0, 1);
  genLog(dryRun ? "dry-run 시작 (DB 미접근)" : `INSERT 시작 → ${p.clickhouse.host}:${p.clickhouse.port}`);
  try {
    await api().Generate(p.id, JSON.stringify(buildScenario()), dryRun, !!$("genNaNConfirm")?.checked);
  } catch (err) {
    state.running = false;
    updateInsertButton();
    throw err;
  }
}

function renderRunResult(res) {
  genLog("");
  genLog(`완료: run_id=${res.runId} rows=${res.totalRows}${res.canceled ? " (취소됨)" : ""}${res.error ? " 오류: " + res.error : ""}`);
  for (const d of res.days || []) {
    genLog(`  cp ${d.checkpointId} ${d.date}: planned=${d.planned} inserted=${d.inserted} readback=${d.readbackOk ? "OK" : "FAIL"} ${d.readbackNote}`);
  }
}

async function loadHistory() {
  const list = (await api().History(20)) || [];
  state.historyLoaded = true;
  $("historyRows").innerHTML = list.length
    ? list
        .map((r) => {
          const days = (r.days || [])
            .map((d) => `${d.date}/cp${d.checkpointId}:${d.readbackOk ? "OK" : "FAIL"}`)
            .join(" ");
          return `<tr><td>${esc(r.runId)}</td><td>${esc(r.profile)}</td><td>${r.dryRun}</td><td>${r.totalRows}</td><td>${esc(days)}</td>
            <td><button class="secondary small" data-report-run="${esc(r.runId)}">리포트</button></td></tr>`;
        })
        .join("")
    : '<tr><td colspan="6" class="hint">이력 없음</td></tr>';
}

// ---------- Verify ----------

// 층 판정은 PASS / FAIL / 미검증 3값이다. 백엔드(verify.LayerResult.Verdict)와 같은 규칙이며,
// 특히 "대조 0건 + 불일치 0"을 PASS로 그리지 않는 것이 핵심이다 — 다음 날 아침 검증에서
// hourly가 통째로 보존 창 밖이면 아무것도 안 본 실행이 초록색으로 남는다.
function layerVerdict(lr) {
  if (!lr.ran) return '<span class="hint">생략</span>';
  if (lr.errored) return '<span class="skip">ERROR(대조 불가)</span>';
  if ((lr.mismatches?.length || 0) > 0) return '<span class="fail">FAIL</span>';
  if (!lr.checked) return '<span class="skip">미검증</span>';
  if (!lr.pass) return '<span class="fail">FAIL</span>';
  return '<span class="pass">PASS</span>';
}

function renderVerify(res) {
  const layers = [res.l1Raw, res.l1Mv, res.l2Daily, res.l2Hourly];
  $("verifyLayers").innerHTML = layers
    .map((lr) => {
      return `<tr><td>${esc(lr.name)}</td><td>${lr.ran ? "예" : "생략"}</td><td>${layerVerdict(lr)}</td>
        <td>${lr.checked}</td><td>${lr.skipped || 0}</td><td>${lr.mismatches?.length || 0}</td><td>${esc(lr.note || "")}</td></tr>`;
    })
    .join("");
  const banner = [];
  banner.push(`복제 지연 가드: ${res.guardPassed ? "통과" : "차단"} (delay ${res.replicationDelay}초)`);
  if (res.excludeDates?.length) banner.push(`exclude_date 등재: ${res.excludeDates.join(", ")}`);
  for (const w of res.warnings || []) banner.push(`주의: ${w}`);
  const anyFail = layers.some((lr) => lr.ran && !lr.errored && (lr.mismatches?.length || 0) > 0);
  const overall = res.pass ? "PASS" : res.inconclusive && !anyFail ? "미검증 — 대조하지 못한 층이 있습니다" : "FAIL";
  banner.push(`전체 판정: ${overall}`);
  const skipped = layers.reduce((n, lr) => n + (lr.skipped || 0), 0);
  if (skipped) banner.push(`보존 창 밖이라 대조에서 제외: ${skipped}개 시간대(정상 부재 — 통과가 아니라 미검증)`);
  $("verifyBanner").textContent = banner.join("  |  ");

  const all = layers.flatMap((lr) => lr.mismatches || []);
  $("mismatchRows").innerHTML = all.length
    ? all
        .map(
          (m) => `<tr><td>${esc(m.layer)}</td><td>${m.cp}</td><td>${esc(m.date)}</td>
          <td>${m.hour < 0 ? "일별" : m.hour}</td><td>${esc(m.field)}</td>
          <td>${esc(m.expected)}</td><td>${esc(m.actual)}</td><td>${esc(m.note || "")}</td></tr>`,
        )
        .join("")
    : '<tr><td colspan="8" class="hint">불일치 없음</td></tr>';
  // "불일치 없음"만 보고 통과로 읽지 않게, 대조를 한 건도 못 한 경우를 같은 자리에 적어 준다.
  if (!all.length && res.inconclusive) {
    $("mismatchRows").innerHTML =
      '<tr><td colspan="8" class="hint">대조를 수행하지 못한 층이 있습니다 — 위 표의 "미검증" 행과 비고를 보세요.</td></tr>';
  }
  state.verifyDone = true;
  updateStepStates();
}

// ---------- E2E ----------

function e2eLog(line) {
  const el = $("e2eLog");
  el.textContent += line + "\n";
  el.scrollTop = el.scrollHeight;
}

function updateE2EButtons() {
  const p = currentProfile();
  const skipGen = $("e2eSkipGen").checked;
  // 주입 포함이면 TestOnly + 확인 체크 필요, 검증만이면 바로 가능
  const ready = !e2eState.running && !state.running && !!p && (skipGen || (p.testOnly && $("e2eConfirm").checked));
  $("e2eStart").disabled = !ready;
  // 실제 INSERT를 포함할 때만 위험색 — 검증만 모드는 일반색
  $("e2eStart").classList.toggle("danger", !skipGen);
  $("e2eStart").classList.toggle("primary", skipGen);
  $("e2eCancel").disabled = !e2eState.running;
  $("e2eSaveMd").disabled = !e2eState.hasResult;
}

const e2eStatusLabel = {
  wait: "대기",
  recheck: "재확인",
  pass: '<span class="pass">PASS</span>',
  fail: '<span class="fail">FAIL</span>',
  skip: "제외",
};
const e2eChipLabel = { pass: "PASS", fail: "FAIL", recheck: "재확인", wait: "대기", skip: "제외" };

function renderE2ECells(cells) {
  e2eState.cells = cells || [];
  const counts = { pass: 0, fail: 0, recheck: 0, wait: 0, skip: 0 };
  for (const c of e2eState.cells) counts[c.status] = (counts[c.status] || 0) + 1;

  // 요약 칩 — 클릭하면 해당 상태만 필터
  $("e2eChips").innerHTML = ["pass", "fail", "recheck", "wait", "skip"]
    .map(
      (k) =>
        `<button class="chip chip-${k} ${e2eState.filter === k ? "active" : ""}" data-chip="${k}">${e2eChipLabel[k]} ${counts[k] || 0}</button>`,
    )
    .join("");

  // skip은 "판정한 셀"이 아니라 "대조하지 못한 셀"이다. 완료로 세면 pass 0인
  // 실행도 진행률 100%로 보인다. 판정(pass/fail)만 진행으로 센다.
  const judged = e2eState.cells.filter((c) => c.status === "pass" || c.status === "fail").length;
  const total = e2eState.cells.length;
  const pct = total ? Math.min(100, (judged / total) * 100) : 0;
  $("e2eProgressBar").style.width = pct + "%";
  $("e2eProgressBar").classList.toggle("bar-warn", counts.skip > 0 && judged === 0);

  // 다음 판정 대상(가장 빠른 pending due) 강조
  let nextKey = null;
  for (const c of e2eState.cells) {
    if ((c.status === "wait" || c.status === "recheck") && (nextKey === null || c.dueRFC < nextKey.dueRFC)) nextKey = c;
  }

  const filter = e2eState.filter;
  const visible = (c) => {
    if (!filter) return true;
    if (filter === "problem") return c.status === "fail" || c.status === "recheck";
    return c.status === filter;
  };

  $("e2eCells").innerHTML = e2eState.cells.length
    ? e2eState.cells
        .map((c) => {
          const isNext = nextKey && c === nextKey;
          return `<tr class="cell-${c.status}${isNext ? " cell-next" : ""}" style="${visible(c) ? "" : "display:none"}">
          <td>${c.cp}</td><td>${esc(c.date)}</td><td>${c.hour < 0 ? "일별" : c.hour}</td>
          <td>${esc(c.checkedAt || c.due)}</td><td>${e2eStatusLabel[c.status] || esc(c.status)}</td>
          <td>${c.attempts}</td><td>${esc(c.note || "")}</td></tr>`;
        })
        .join("")
    : '<tr><td colspan="7" class="hint">셀 없음</td></tr>';
}

function setE2EFilter(f) {
  e2eState.filter = e2eState.filter === f ? null : f;
  $("e2eProblemOnly").classList.toggle("active", e2eState.filter === "problem");
  renderE2ECells(e2eState.cells);
}

function renderPreflight(pf) {
  $("e2ePreflightPanel").style.display = "";
  const row = (ok, label, msg) =>
    `<div class="pf-row ${ok ? "pf-ok" : "pf-bad"}"><span class="pf-label">${label}</span><span>${esc(msg)}</span></div>`;
  const parts = [];
  parts.push(row(pf.mariaOk, "MariaDB", pf.mariaOk ? "연결 OK — " + pf.mariaMsg : pf.mariaMsg));
  parts.push(row(pf.chOk, "ClickHouse", pf.chOk ? "연결 OK — " + pf.chMsg : pf.chMsg));
  parts.push(row(pf.testOnly, "TestOnly", pf.testOnly ? "INSERT 허용" : "INSERT 차단 세션"));
  parts.push(row(pf.today, "오늘 날짜", pf.today ? "시나리오에 포함" : "미포함 — E2E는 오늘 날짜 전용"));
  if (pf.plan) {
    const est = pf.plan.lastDue ? new Date(pf.plan.lastDue) : null;
    const text = `총 ${pf.plan.totalCells}개 (제외 ${pf.plan.skipCells})${est ? ` · 예상 종료 ~${est.getMonth() + 1}/${est.getDate()} ${hm(est)}` : ""}`;
    if (pf.plan.skipCells >= pf.plan.totalCells) {
      parts.push(row(false, "셀 계획", text));
    } else if (pf.plan.skipCells > 0) {
      // 제외 셀이 있으면 그만큼 검증되지 않는다 — 초록으로 보이면 안 된다.
      parts.push(`<div class="pf-row pf-warn"><span class="pf-label">셀 계획</span><span>${esc(text)} — 제외분은 검증되지 않습니다</span></div>`);
    } else {
      parts.push(row(true, "셀 계획", text));
    }
  }
  for (const w of pf.warnings || []) parts.push(`<div class="pf-row pf-warn"><span class="pf-label">주의</span><span>${esc(w)}</span></div>`);
  for (const f of pf.fatal || []) parts.push(`<div class="pf-row pf-bad"><span class="pf-label">차단</span><span>${esc(f)}</span></div>`);
  $("e2ePreflight").innerHTML = parts.join("");
}

function stopCountdown() {
  if (e2eState.timer) {
    clearInterval(e2eState.timer);
    e2eState.timer = null;
  }
}

function startCountdown() {
  stopCountdown();
  const tick = () => {
    if (!e2eState.nextAtRFC) return;
    const next = new Date(e2eState.nextAtRFC);
    const est = e2eState.estEndRFC ? ` · 예상 종료 ~${hm(new Date(e2eState.estEndRFC))}` : "";
    const ms = next - Date.now();
    $("e2eStatus").textContent =
      ms <= 0 ? `판정 시점 도래 — verify 실행 대기${est}` : `다음 판정 ${hm(next)} (${dur(ms)} 남음)${est}`;
  };
  tick();
  e2eState.timer = setInterval(tick, 1000);
}

function hideResumeBanner() {
  $("e2eResume").style.display = "none";
  e2eState.pendingNote = null;
}

// confirmDiscard는 두 번 눌러야 실행되는 확인이다. 저장 상태를 버리면 되돌릴 수 없고
// (보존 창이 지난 셀은 재판정 불가) 네이티브 모달은 WebView를 멈추게 하므로 인라인으로 처리한다.
let discardArmed = false;
function confirmDiscard() {
  const btn = $("resumeDiscard");
  if (discardArmed) return true;
  discardArmed = true;
  const original = btn.textContent;
  btn.textContent = "되돌릴 수 없습니다 — 다시 클릭";
  btn.classList.add("danger");
  setTimeout(() => {
    discardArmed = false;
    const b = $("resumeDiscard");
    if (b) {
      b.textContent = original;
      b.classList.remove("danger");
    }
  }, 5000);
  return false;
}

async function checkPendingE2E() {
  const st = await api().PendingE2E();
  if (!st?.exists) return;
  const banner = $("e2eResume");
  banner.style.display = "";
  // 배너는 E2E 탭 안에만 있다 — 상태 배지로도 알려 다른 탭에서 온 사용자가 놓치지 않게.
  // (세션 전환 리셋이 배지를 "대기"로 되돌리지 않도록 상태로도 보관)
  e2eState.pendingNote = "미완료 저장 상태 있음 — 상단 배너에서 이어서/버리기";
  $("e2eStatus").textContent = e2eState.pendingNote;
  // 대상 DB를 함께 보여준다 — 다른 프로파일로 이어받으면 주입되지 않은 DB에서
  // L1이 불합격하고, 그 중단이 저장된 판정분을 위협한다.
  const target = st.target ? ` · 대상 ${esc(st.target)}` : "";
  const noCells = !!st.noCells;
  banner.innerHTML = `<span>미완료 E2E 저장 상태가 있습니다 — 시작 ${esc(st.startedAt || "?")} · 셀 ${st.doneCells}/${st.totalCells} 판정 완료${st.aborted ? " · 중단: " + esc(st.aborted) : ""}${st.profile ? " · 세션 " + esc(st.profile) : ""}${target}${
    noCells ? " <b>· 셀 계획 이전에 중단되어 재개할 수 없습니다 (처음부터 실행하세요)</b>" : ""
  }</span>
    <span class="resume-actions">
      <button id="resumeGo" class="primary small"${noCells ? " disabled" : ""}>이어서 하기</button>
      <button id="resumeDiscard" class="secondary small">상태 버리기</button>
    </span>`;
  if (!noCells) {
    $("resumeGo").addEventListener(
      "click",
      guard(async () => {
        if (st.scenarioJson) applyScenario(JSON.parse(st.scenarioJson));
        setSection("e2e");
        await startE2E(true);
      }),
    );
  }
  $("resumeDiscard").addEventListener(
    "click",
    guard(async () => {
      // 되돌릴 수 없다. 보존 창이 지난 셀은 다시 판정할 수 없으므로 한 번 묻는다.
      if (!confirmDiscard()) return;
      await api().DiscardE2EState();
      hideResumeBanner();
    }),
  );
}

async function startE2E(resume = false) {
  const p = currentProfile();
  if (!p) throw new Error("세션을 선택하세요.");
  const scenarioJSON = JSON.stringify(buildScenario());
  const skipGen = resume || $("e2eSkipGen").checked;

  // 사전 점검 — 과거 날짜 시나리오로 하루치 INSERT를 낭비하는 실수를 시작 전에 잡는다
  $("e2eStatus").textContent = "사전 점검 중";
  const pf = await api().E2EPreflight(p.id, scenarioJSON, skipGen, $("e2eSkipDaily").checked);
  renderPreflight(pf);
  if (pf.fatal?.length) {
    $("e2eStatus").textContent = "사전 점검 실패";
    throw new Error("사전 점검 차단 항목이 있습니다 — 점검 카드를 확인하고 해결 후 다시 시작하세요.");
  }

  e2eState.running = true;
  e2eState.hasResult = false;
  updateE2EButtons();
  updateInsertButton();
  $("e2eLog").textContent = "";
  $("e2eStatus").textContent = "실행 중";
  hideResumeBanner();
  e2eLog(`E2E 시작 → ${p.name} (${p.clickhouse?.host || "?"})${resume ? " [저장 상태 재개]" : ""}`);
  const opts = {
    skipGenerate: skipGen,
    resume,
    keepGoing: $("e2eKeepGoing").checked,
    skipDaily: $("e2eSkipDaily").checked,
    retryMinutes: Number($("e2eRetryMin").value) || 0,
    maxAttempts: Number($("e2eMaxAttempts").value) || 0,
  };
  try {
    await api().RunE2E(p.id, scenarioJSON, JSON.stringify(opts));
  } catch (err) {
    e2eState.running = false;
    updateE2EButtons();
    updateInsertButton();
    throw err;
  }
}

// ---------- Report ----------

let lastReport = null;

async function buildReport() {
  lastReport = await api().BuildReport();
  $("reportText").value = lastReport.markdown;
  state.reportDone = true;
  updateStepStates();
}

// ---------- 이벤트 바인딩 ----------

document.querySelectorAll(".step").forEach((b) => b.addEventListener("click", () => setSection(b.dataset.section)));
$("errorClose").addEventListener("click", clearError);

// 새 세션 = 연결 단계의 작업 — 어느 화면에서 눌러도 입력 폼이 있는 곳으로 데려간다
$("newProfile").addEventListener("click", () => {
  if (blockDuringRun("새 세션")) return;
  startNewSessionDraft();
  setSection("connections");
  $("pfName").focus();
});
bindBusy("saveProfile", async () => {
  const saved = await api().SaveProfile(profileFromForm());
  await loadProfiles(saved.id);
}, "저장 중...");
$("deleteProfile").addEventListener(
  "click",
  guard(async () => {
    if (!state.profileId) return;
    if (blockDuringRun("세션 삭제")) return;
    try {
      localStorage.removeItem(scenarioKeyFor(state.profileId));
    } catch {}
    await api().DeleteProfile(state.profileId);
    await loadProfiles();
  }),
);
// 설정 메뉴 — 기어 버튼 토글, 바깥 클릭·Esc로 닫는다(메뉴 안 클릭은 유지)
function closeSettingsMenu() {
  $("settingsMenu").hidden = true;
  $("settingsBtn").classList.remove("open");
  $("settingsBtn").setAttribute("aria-expanded", "false");
}
$("settingsBtn").addEventListener("click", (e) => {
  e.stopPropagation();
  const open = $("settingsMenu").hidden;
  $("settingsMenu").hidden = !open;
  $("settingsBtn").classList.toggle("open", open);
  $("settingsBtn").setAttribute("aria-expanded", String(open));
});
$("settingsMenu").addEventListener("click", (e) => e.stopPropagation());
document.addEventListener("click", () => {
  if (!$("settingsMenu").hidden) closeSettingsMenu();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("settingsMenu").hidden) closeSettingsMenu();
});
// 데이터 소스 추가 메뉴 — 기어 메뉴와 같은 문법(토글, 바깥 클릭·Esc 닫힘)
$("dsAddBtn").addEventListener("click", (e) => {
  e.stopPropagation();
  const open = $("dsAddMenu").hidden;
  $("dsAddMenu").hidden = !open;
  $("dsAddBtn").setAttribute("aria-expanded", String(open));
});
$("dsAddMenu").addEventListener("click", (e) => {
  e.stopPropagation();
  const item = e.target.closest(".ds-menu-item");
  if (!item || item.disabled) return;
  const k = item.dataset.ds;
  dsState.added[k] = true;
  dsState.selected = k;
  closeDsMenu();
  renderDataSources();
  $(DS_META[k].hostId).focus();
});
document.addEventListener("click", () => {
  if (!$("dsAddMenu").hidden) closeDsMenu();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !$("dsAddMenu").hidden) closeDsMenu();
});
$("dsList").addEventListener("click", (e) => {
  const row = e.target.closest(".ds-row");
  if (!row) return;
  dsState.selected = row.dataset.ds;
  renderDataSources();
});
$("dsRemoveMaria").addEventListener("click", (e) => armThenRun(e.currentTarget, () => removeDataSource("maria")));
$("dsRemoveCh").addEventListener("click", (e) => armThenRun(e.currentTarget, () => removeDataSource("ch")));
// 호스트·포트·DB를 고치면 목록 행 요약도 따라간다
for (const k of ["maria", "ch"]) {
  for (const id of [DS_META[k].hostId, DS_META[k].portId, DS_META[k].dbId]) {
    $(id).addEventListener("input", renderDataSources);
  }
}
// 테마 선택 — data-theme 토큰 스위치(head의 부트스트랩 스크립트가 첫 페인트 전 적용)
$("themeSelect").addEventListener("change", () => {
  const t = $("themeSelect").value;
  document.documentElement.dataset.theme = t;
  try {
    localStorage.setItem("rawgen.theme", t);
  } catch {}
});
bindBusy("dupProfile", async () => {
  if (!state.profileId) throw new Error("복제할 세션을 먼저 고르세요");
  if (blockDuringRun("복제")) return;
  const copy = await api().DuplicateProfile(state.profileId);
  await loadProfiles(copy.id);
  // 복제 직후 할 일 = 이름·호스트 고치기 — 그 폼이 있는 연결 단계로
  setSection("connections");
  $("pfName").focus();
}, "복제 중...");
function showImportSummary(sum) {
  const parts = [];
  if (sum.added?.length) parts.push(`추가 ${sum.added.length}건: ${sum.added.join(", ")}`);
  if (sum.updated?.length) parts.push(`갱신 ${sum.updated.length}건: ${sum.updated.join(", ")}`);
  for (const w of sum.warnings || []) parts.push("주의: " + w);
  $("importResult").textContent = parts.length ? parts.join(" | ") : "가져온 세션이 없습니다.";
}
bindBusy("importProfilesFile", async () => {
  const sum = await api().ImportProfilesFile();
  showImportSummary(sum);
  await loadProfiles();
}, "가져오는 중...");
bindBusy("importProfilesPaste", async () => {
  const sum = await api().ImportProfiles($("importProfilesText").value);
  showImportSummary(sum);
  $("importProfilesText").value = "";
  await loadProfiles();
}, "가져오는 중...");
bindBusy("exportProfilesFile", async (btn) => {
  const path = await api().ExportProfilesFile();
  if (path) flashButton(btn, "저장됨");
  $("importResult").textContent = path ? `저장: ${path} (비밀번호는 자리표시자)` : "저장을 취소했습니다.";
}, "저장 중...");
bindBusy("exportProfiles", async (btn) => {
  const text = await api().ExportProfiles();
  await navigator.clipboard.writeText(text);
  $("discoverOut").textContent = "클립보드에 복사됨 (비밀번호는 placeholder 치환):\n\n" + text;
  flashButton(btn, "복사됨");
});
function recordConn(id, key, ok) {
  state.connCheck[id] = { ...(state.connCheck[id] || {}), [key]: ok };
  renderSessionList();
  renderDataSources(); // 데이터 소스 행의 연결 점도 같은 결과를 본다
}
bindBusy("testMaria", async () => {
  const saved = await api().SaveProfile(profileFromForm());
  await loadProfiles(saved.id);
  const r = await api().TestMaria(saved.id);
  recordConn(saved.id, "maria", !!r.ok);
  $("discoverOut").textContent = (r.ok ? "OK: " : "실패: ") + r.message;
}, "연결 확인 중...");
bindBusy("testCh", async () => {
  const saved = await api().SaveProfile(profileFromForm());
  await loadProfiles(saved.id);
  const r = await api().TestCH(saved.id);
  recordConn(saved.id, "ch", !!r.ok);
  $("discoverOut").textContent = (r.ok ? "OK: " : "실패: ") + r.message;
}, "연결 확인 중...");
bindBusy("discover", async () => {
  const d = await api().Discover(state.profileId);
  const lines = [];
  lines.push(`checkvalue engine: ${d.checkvalueEngine} (replicated=${d.replicated})`);
  lines.push(`log_date type: ${d.dateTimePrecision}`);
  lines.push(`MV max_time 컬럼: ${d.mvHasMaxTime}`);
  if (d.replicationDelay >= 0) {
    lines.push(
      `복제 지연: ${d.replicationDelay}초${d.replicationDelay > 60 ? " — verify 가드(60초)에 차단됩니다. 지연 해소 후 검증하세요" : ""}`,
    );
  } else {
    lines.push("복제 지연: 측정 불가(비복제 환경이면 정상)");
  }
  if (d.excludeDates?.length) lines.push(`exclude_date(최근): ${d.excludeDates.join(", ")}`);
  for (const p of d.problems || []) lines.push(`문제: ${p}`);
  lines.push("", "checkvalue columns:");
  for (const c of d.checkvalueColumns || []) lines.push(`  ${c.name}  ${c.type}`);
  $("discoverOut").textContent = lines.join("\n");
}, "확인 중...");

bindBusy("cpLoad", async () => {
  const res = await api().ListCheckpoints(state.profileId, $("cpSearch").value.trim());
  state.checkpoints = res.items || [];
  renderCheckpointTable();
}, "조회 중...");
$("cpSearch").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("cpLoad").click();
});
$("cpRows").addEventListener("change", (e) => {
  const id = Number(e.target.dataset.cp);
  if (!id) return;
  if (e.target.checked) state.selectedCps.add(id);
  else state.selectedCps.delete(id);
  renderSelectedCps();
});
$("cpAdd").addEventListener("click", () => {
  const id = Number($("cpManual").value);
  if (id > 0) {
    state.selectedCps.add(id);
    $("cpManual").value = "";
    renderSelectedCps();
  }
});
$("cpManual").addEventListener("keydown", (e) => {
  if (e.key === "Enter") $("cpAdd").click();
});
$("cpSelected").addEventListener("click", (e) => {
  const id = Number(e.target.dataset.removeCp);
  if (id) {
    e.preventDefault();
    state.selectedCps.delete(id);
    renderSelectedCps();
    renderCheckpointTable();
  }
});

$("addHour").addEventListener("click", () => {
  state.overrides.push({ hour: 12, mode: "goal", goal: { min: 20, max: 40, avg: 30 }, nanCount: 0 });
  renderHourRows();
});
// input마다 재렌더하면 포커스가 소실됨 — 상태만 갱신, hour 라벨 셀만 부분 갱신
$("hourRows").addEventListener("input", (e) => {
  const i = Number(e.target.dataset.oi);
  const f = e.target.dataset.of;
  if (!Number.isInteger(i) || !f) return;
  if (f === "mode") state.overrides[i].mode = e.target.value;
  else if (f === "nanCount") state.overrides[i].nanCount = Math.max(0, Number(e.target.value) || 0);
  else if (f === "hour") {
    state.overrides[i].hour = Number(e.target.value);
    const cell = document.querySelector(`[data-maria-cell="${i}"]`);
    const h = Number(e.target.value);
    if (cell) cell.textContent = Number.isInteger(h) && h >= 0 && h <= 23 ? toMariaHour(h) : "-";
  } else state.overrides[i].goal[f] = Number(e.target.value);
  refreshScenarioJson();
});
$("hourRows").addEventListener("click", (e) => {
  const i = Number(e.target.dataset.removeHour);
  if (Number.isInteger(i)) {
    state.overrides.splice(i, 1);
    renderHourRows();
  }
});
for (const id of ["startDate", "endDate", "intervalSec", "driverCode", "seed", "batchSize", "dailyMin", "dailyMax", "dailyAvg"]) {
  $(id).addEventListener("input", refreshScenarioJson);
}
// 시나리오 폼 어디서든 Ctrl+Enter = Preview 실행
$("scenario").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
    setSection("preview");
    $("runPreview").click();
  }
});

$("copyScenario").addEventListener(
  "click",
  guard(async () => {
    await navigator.clipboard.writeText($("scenarioJson").textContent);
    flashButton($("copyScenario"), "복사됨");
  }),
);
bindBusy("scnLoad", async (btn) => {
  const res = await api().LoadScenarioFile();
  if (!res) return; // 사용자가 대화상자 취소
  applyScenario(res.scenario);
  const notes = [];
  if (res.problem) notes.push("시나리오 오류: " + res.problem);
  for (const w of res.warnings || []) notes.push(w);
  if (notes.length) showError(`불러옴(${res.path}) — ${notes.join(" / ")}`);
  flashButton(btn, "불러옴");
});
bindBusy("scnSave", async () => {
  const name = (state.scenarioName && state.scenarioName !== "gui" ? state.scenarioName : "scenario") + ".json";
  await api().SaveReportFile($("scenarioJson").textContent, name);
});

bindBusy("runPreview", async () => {
  const pv = await api().BuildPreview(JSON.stringify(buildScenario()));
  renderPreview(pv);
}, "계산 중...");
$("hourlyDaySelect").addEventListener("change", () => renderHourly(Number($("hourlyDaySelect").value)));
$("copyDaily").addEventListener("click", guard(async () => copyTableTSV($("dailyExpectedTable"), $("copyDaily"))));

$("genConfirm").addEventListener("change", updateInsertButton);
$("genNaNConfirm").addEventListener("change", updateInsertButton);
$("runDry").addEventListener("click", guard(() => startRun(true)));
$("runInsert").addEventListener("click", guard(() => startRun(false)));
$("cancelRun").addEventListener(
  "click",
  guard(async () => {
    await api().CancelGenerate();
    genLog("취소 요청됨 (batch 경계에서 중단)");
  }),
);
bindBusy("loadHistory", loadHistory, "불러오는 중...");
$("historyRows").addEventListener(
  "click",
  guard(async (e) => {
    const id = e.target.dataset.reportRun;
    if (!id) return;
    const md = await api().ReportFromHistory(id);
    lastReport = { markdown: md, json: "", csv: "" };
    $("reportText").value = md;
    state.reportDone = true;
    setSection("report");
  }),
);

bindBusy("buildChecklist", async () => {
  const text = await api().RegenChecklist(state.profileId, JSON.stringify(buildScenario()));
  $("checklistText").value = text;
  updateStepStates();
}, "생성 중...");
$("copyChecklist").addEventListener(
  "click",
  guard(async () => {
    await navigator.clipboard.writeText($("checklistText").value);
    flashButton($("copyChecklist"), "복사됨");
  }),
);

bindBusy("verifyL1", async () => {
  const res = await api().RunVerify(state.profileId, JSON.stringify(buildScenario()), true);
  renderVerify(res);
}, "검증 중 — 최대 1분 대기");
bindBusy("verifyAll", async () => {
  const res = await api().RunVerify(state.profileId, JSON.stringify(buildScenario()), false);
  renderVerify(res);
}, "검증 중 — 최대 1분 대기");
$("copyMismatch").addEventListener("click", guard(async () => copyTableTSV($("mismatchTable"), $("copyMismatch"))));

$("e2eSkipGen").addEventListener("change", updateE2EButtons);
$("e2eConfirm").addEventListener("change", updateE2EButtons);
$("e2eStart").addEventListener("click", guard(() => startE2E(false)));
$("e2eCancel").addEventListener(
  "click",
  guard(async () => {
    await api().CancelE2E();
    e2eLog("중단 요청됨 — 지금까지의 셀 판정은 저장되어 있어 나중에 이어서 할 수 있습니다");
  }),
);
bindBusy("e2eSaveMd", async () => {
  const md = await api().E2EReport();
  await api().SaveReportFile(md, "rawgen-e2e-report.md");
});
$("e2eChips").addEventListener("click", (e) => {
  const f = e.target.dataset.chip;
  if (f) setE2EFilter(f);
});
$("e2eProblemOnly").addEventListener("click", () => setE2EFilter("problem"));
$("copyCells").addEventListener("click", guard(async () => copyTableTSV($("e2eCellsTable"), $("copyCells"))));

bindBusy("buildReport", buildReport, "생성 중...");
$("copyReport").addEventListener(
  "click",
  guard(async () => {
    await navigator.clipboard.writeText($("reportText").value);
    flashButton($("copyReport"), "복사됨");
  }),
);
bindBusy("saveMd", async () => {
  if (!lastReport) await buildReport();
  await api().SaveReportFile(lastReport.markdown, "rawgen-report.md");
});
bindBusy("saveJson", async () => {
  if (!lastReport) await buildReport();
  if (!lastReport.json) throw new Error("JSON 리포트가 없습니다. [생성]을 먼저 실행하세요.");
  await api().SaveReportFile(lastReport.json, "rawgen-report.json");
});
bindBusy("saveCsv", async () => {
  if (!lastReport) await buildReport();
  if (!lastReport.csv) throw new Error("검증 결과가 없어 CSV를 만들 수 없습니다.");
  await api().SaveReportFile(lastReport.csv, "rawgen-mismatch.csv");
});

// ---------- Wails 이벤트 ----------

function bindRuntimeEvents() {
  if (!rt()) return;
  rt().EventsOn("gen:progress", (pr) => {
    if (pr.message) genLog(`[${pr.phase}] ${pr.message}`);
    if (pr.total > 0) setProgress(pr.done, pr.total);
  });
  rt().EventsOn("gen:done", (res) => {
    state.running = false;
    // readback은 옵션이 아니라 고정 단계다(중복·결손 검출). 실패를 완료로 표시하면
    // 그 뒤 나온 불일치를 제품 버그로 오인 보고하게 된다.
    const days = res?.days || [];
    const rbOk = days.length > 0 && days.every((d) => d.readbackOk);
    state.lastRunOk = !!res && !res.error && !res.canceled && rbOk;
    updateInsertButton();
    updateStepStates();
    if (state.lastRunOk) setProgress(1, 1);
    renderRunResult(res);
    if (res && !res.error && !res.canceled && !rbOk) {
      showError(
        "readback 실패 — 주입이 확인되지 않았습니다. 이 상태의 검증 결과는 제품 판정 근거로 쓸 수 없습니다. 로그의 readback 사유를 확인하고 대상 범위를 정리한 뒤 다시 주입하세요."
      );
    }
    loadHistory().catch(() => {});
  });
  rt().EventsOn("gen:error", (msg) => {
    state.running = false;
    updateInsertButton();
    genLog("오류: " + msg);
    showError(msg);
  });

  rt().EventsOn("e2e:progress", (pr) => {
    if (pr.phase === "cells") {
      renderE2ECells(pr.cells);
      if (!e2eState.timer) $("e2eStatus").textContent = `실행 중 — 셀 ${pr.done}/${pr.total} 판정`;
      return;
    }
    if (pr.phase === "wait") {
      e2eState.nextAtRFC = pr.nextAtRFC || null;
      e2eState.estEndRFC = pr.estEndRFC || null;
      startCountdown();
    } else {
      stopCountdown();
      if (pr.phase === "verify") $("e2eStatus").textContent = "verify 실행 중";
    }
    if (pr.message) e2eLog(`[${pr.phase}] ${pr.message}`);
  });
  rt().EventsOn("e2e:done", (res) => {
    stopCountdown();
    e2eState.running = false;
    e2eState.hasResult = true;
    updateE2EButtons();
    updateInsertButton();
    renderE2ECells(res.cells);
    const c = res.counts || {};
    const verdict = res.aborted
      ? `중단 (${res.aborted})`
      : res.pass
        ? "PASS"
        : res.inconclusive
          ? `미검증 (판정 ${c.pass || 0} / 미검증 ${c.skip || 0}) — 통과로 결재할 수 없음`
          : "FAIL";
    $("e2eStatus").textContent = verdict;
    e2eLog(`E2E 종료: ${verdict} — verify ${res.verifyRuns}회`);
    if (res.finalVerify) renderVerify(res.finalVerify);
    e2eLog("리포트는 자동 저장되었고(로그의 경로 참조), 최종 verify 결과는 검증 탭에 반영되었습니다.");
    if (res.aborted) checkPendingE2E().catch(() => {});
    updateStepStates();
  });
  rt().EventsOn("e2e:error", (msg) => {
    stopCountdown();
    e2eState.running = false;
    updateE2EButtons();
    updateInsertButton();
    $("e2eStatus").textContent = "오류";
    e2eLog("오류: " + msg);
    showError(msg);
    checkPendingE2E().catch(() => {});
  });
}

// ---------- 초기화 ----------

(function init() {
  // 마지막 시나리오 자동 복원 — 없으면 어제 날짜 기본값
  let restored = false;
  try {
    const raw = localStorage.getItem(SCENARIO_STORE_KEY) || localStorage.getItem(SCENARIO_STORE_KEY_LEGACY);
    if (raw) {
      applyScenario(JSON.parse(raw));
      restored = true;
    }
  } catch {}
  if (!restored) {
    const today = new Date();
    const y = new Date(today);
    y.setDate(y.getDate() - 1);
    const dt = (d) =>
      `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
    $("startDate").value = dt(y);
    $("endDate").value = dt(y);
    renderHourRows();
    renderSelectedCps();
  }

  makeSortable($("dailyExpectedTable"));
  makeSortable($("mismatchTable"));
  makeSortable($("e2eCellsTable"));

  renderDataSources();
  updateE2EButtons();
  updateStepStates();
  bindRuntimeEvents();
  $("themeSelect").value = document.documentElement.dataset.theme || "dark";

  // Ctrl+1~9 = 단계 이동 (키보드 우선 — IDE 사용자 기대)
  const stepOrder = [...document.querySelectorAll(".step")].map((el) => el.dataset.section);
  document.addEventListener("keydown", (e) => {
    if (!e.ctrlKey || e.altKey || e.shiftKey || e.metaKey) return;
    const n = Number(e.key);
    if (n >= 1 && n <= stepOrder.length) {
      e.preventDefault();
      setSection(stepOrder[n - 1]);
    }
  });

  if (api()) {
    loadProfiles().catch(showError);
    checkPendingE2E().catch(() => {});
    api()
      .Info()
      .then((i) => {
        if (i?.version) $("stVersion").textContent = "rawgen v" + i.version;
      })
      .catch(() => {});
    // 프런트가 리로드돼도 백엔드 실행 상태를 복원 (E2E는 백엔드에서 계속 돈다)
    api()
      .Busy()
      .then((b) => {
        if (b === "e2e") {
          e2eState.running = true;
          $("e2eStatus").textContent = "실행 중 (백그라운드 진행)";
          updateE2EButtons();
          updateInsertButton();
        }
      })
      .catch(() => {});
  } else {
    showError("Wails 바인딩이 없습니다. 브라우저 미리보기에서는 DB 기능을 쓸 수 없습니다.");
  }
})();
