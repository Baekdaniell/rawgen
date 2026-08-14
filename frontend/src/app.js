// rawgen frontend — Wails 바인딩(window.go.main.App) 호출 담당.
// 계산/DB 로직은 전부 Go 코어에 있고 여기는 표시만 한다.
const $ = (id) => document.getElementById(id);
const api = () => window.go?.main?.App;
const rt = () => window.runtime;

const SCENARIO_STORE_KEY = "rawgen.lastScenario";
const SCENARIO_STORE_KEY_LEGACY = "sg.lastScenario"; // 개명 전 키 (읽기 전용 폴백)

const state = {
  profiles: [],
  profileId: "",
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
};

const sectionMeta = {
  connections: ["Connections", "DB 연결 프로파일을 만들고 테스트합니다."],
  targets: ["Targets", "데이터를 주입할 checkpoint를 고릅니다."],
  scenario: ["Scenario", "날짜, 주기, 목표 통계값을 입력합니다."],
  preview: ["Preview", "생성될 데이터와 기대 통계를 확인합니다 (DB 미접근)."],
  generate: ["Generate", "ClickHouse에 batch INSERT + readback을 실행합니다."],
  regen: ["Regenerate", "제품 통계 재생성 유도 절차를 안내합니다."],
  verify: ["Verify", "L1(주입 직후) / L2(재생성 후) 검증을 실행합니다."],
  e2e: ["E2E 자동", "주입→L1→시간대별 자동 verify→리포트를 한 번에 수행합니다."],
  report: ["Report", "결과를 Markdown/JSON/CSV로 정리합니다."],
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
    checkvalueTable: "",
    dailyStatsChTable: "",
    dailyStatsTable: "",
    hourlyTable: "",
    checkpointTable: "",
    excludeDateTable: "",
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
  renderSideProfile();
}

function currentProfile() {
  return state.profiles.find((p) => p.id === state.profileId);
}

function renderSideProfile() {
  const p = currentProfile();
  $("sideProfile").textContent = p ? `${p.name} (${p.clickhouse?.host || "?"})` : "선택 안 됨";
  $("sideTestOnly").textContent = p ? (p.testOnly ? "TestOnly: INSERT 허용" : "TestOnly 아님: INSERT 차단") : "";
  updateInsertButton();
  updateE2EButtons();
  updateStepStates();
}

async function loadProfiles(selectId) {
  state.profiles = (await api().ListProfiles()) || [];
  const sel = $("profileSelect");
  sel.innerHTML = state.profiles
    .map((p) => `<option value="${esc(p.id)}">${esc(p.name)}</option>`)
    .join("");
  if (selectId) sel.value = selectId;
  const chosen = state.profiles.find((p) => p.id === sel.value);
  if (chosen) fillForm(chosen);
  else renderSideProfile();
}

// ---------- Targets ----------

function renderSelectedCps() {
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
    })),
    seed: Number($("seed").value),
    batchSize: Number($("batchSize").value) || 100000,
    driverCode: $("driverCode").value,
  };
}

function refreshScenarioJson() {
  const s = buildScenario();
  $("scenarioJson").textContent = JSON.stringify(s, null, 2);
  try {
    localStorage.setItem(SCENARIO_STORE_KEY, JSON.stringify(s));
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
  if (s.startDate) $("startDate").value = s.startDate;
  if (s.endDate) $("endDate").value = s.endDate;
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
  }));
  renderHourRows();
  renderSelectedCps();
  if (state.checkpoints.length) renderCheckpointTable();
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
    `프로파일: ${p ? p.name : "(미선택)"}  TestOnly=${p ? p.testOnly : "-"}`,
    `ClickHouse: ${p?.clickhouse?.host || "?"}:${p?.clickhouse?.port || "?"} / ${p?.clickhouse?.database || "?"} → ${p?.checkvalueTable || "checkvalue"}`,
    `날짜: ${s.startDate} .. ${s.endDate} (${s.timezone})`,
    `checkpoint ${s.checkpointIds.length}개: ${s.checkpointIds.join(", ")}`,
    `예상 row: ${pv.totalRows.toLocaleString()} / batch ${Math.ceil(pv.totalRows / bs)}개 (size ${bs.toLocaleString()})`,
    `경고 ${pv.warnings?.length || 0}건${pv.warnings?.length ? " — Preview 탭에서 확인" : ""}`,
    ``,
    `주입 후 이 (날짜 × cp)는 다른 검증의 클린 대조 기준으로 사용 금지.`,
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
      return `<tr><td>${h.hour}</td><td>${h.dailyCol}</td><td>${h.hourlyCol}</td>
        <td>${fmt(st.count, 0)}</td><td>${st.count ? fmt(st.min, 3) : "NULL"}</td>
        <td>${st.count ? fmt(st.max, 3) : "NULL"}</td><td>${st.count ? fmt(st.avg, 6) : "NULL"}</td>
        <td>${esc(st.maxTime || "NULL")}</td></tr>`;
    })
    .join("");
}

// ---------- Generate ----------

function updateInsertButton() {
  const p = currentProfile();
  const anyRun = state.running || e2eState.running;
  const ok = !!state.preview && !!p?.testOnly && $("genConfirm").checked && !anyRun;
  $("runInsert").disabled = !ok;
  $("runDry").disabled = anyRun;
  $("cancelRun").disabled = !state.running;
  // E2E 대기 중 같은 (cp×날짜)에 추가 INSERT·동시 verify는 expected/결과를 오염시킴
  $("verifyL1").disabled = e2eState.running;
  $("verifyAll").disabled = e2eState.running;
  $("sideBusy").textContent = e2eState.running
    ? "E2E 실행 중 — Generate/Verify 잠김"
    : state.running
      ? "Generate 실행 중"
      : "";
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
  if (!p) throw new Error("프로파일을 선택하세요.");
  state.running = true;
  updateInsertButton();
  $("genLog").textContent = "";
  setProgress(0, 1);
  genLog(dryRun ? "dry-run 시작 (DB 미접근)" : `INSERT 시작 → ${p.clickhouse.host}:${p.clickhouse.port}`);
  try {
    await api().Generate(p.id, JSON.stringify(buildScenario()), dryRun);
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

function renderVerify(res) {
  const layers = [res.l1Raw, res.l1Mv, res.l2Daily, res.l2Hourly];
  $("verifyLayers").innerHTML = layers
    .map((lr) => {
      const status = !lr.ran ? "-" : lr.pass ? '<span class="pass">PASS</span>' : '<span class="fail">FAIL</span>';
      return `<tr><td>${esc(lr.name)}</td><td>${lr.ran ? "예" : "생략"}</td><td>${status}</td>
        <td>${lr.checked}</td><td>${lr.mismatches?.length || 0}</td><td>${esc(lr.note || "")}</td></tr>`;
    })
    .join("");
  const banner = [];
  banner.push(`복제 지연 가드: ${res.guardPassed ? "통과" : "차단"} (delay ${res.replicationDelay}초)`);
  if (res.excludeDates?.length) banner.push(`exclude_date 등재: ${res.excludeDates.join(", ")}`);
  for (const w of res.warnings || []) banner.push(`주의: ${w}`);
  banner.push(`전체 판정: ${res.pass ? "PASS" : "FAIL"}`);
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

  const done = e2eState.cells.filter((c) => c.status !== "wait" && c.status !== "recheck").length;
  const total = e2eState.cells.length;
  const pct = total ? Math.min(100, (done / total) * 100) : 0;
  $("e2eProgressBar").style.width = pct + "%";

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
  parts.push(row(pf.testOnly, "TestOnly", pf.testOnly ? "INSERT 허용" : "INSERT 차단 프로파일"));
  parts.push(row(pf.today, "오늘 날짜", pf.today ? "시나리오에 포함" : "미포함 — E2E는 오늘 날짜 전용"));
  if (pf.plan) {
    const est = pf.plan.lastDue ? new Date(pf.plan.lastDue) : null;
    parts.push(
      row(
        pf.plan.skipCells < pf.plan.totalCells,
        "셀 계획",
        `총 ${pf.plan.totalCells}개 (제외 ${pf.plan.skipCells})${est ? ` · 예상 종료 ~${est.getMonth() + 1}/${est.getDate()} ${hm(est)}` : ""}`,
      ),
    );
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
}

async function checkPendingE2E() {
  const st = await api().PendingE2E();
  if (!st?.exists) return;
  const banner = $("e2eResume");
  banner.style.display = "";
  banner.innerHTML = `<span>미완료 E2E 저장 상태가 있습니다 — 시작 ${esc(st.startedAt || "?")} · 셀 ${st.doneCells}/${st.totalCells} 판정 완료${st.aborted ? " · 중단: " + esc(st.aborted) : ""}</span>
    <span class="resume-actions">
      <button id="resumeGo" class="primary small">이어서 하기</button>
      <button id="resumeDiscard" class="secondary small">상태 버리기</button>
    </span>`;
  $("resumeGo").addEventListener(
    "click",
    guard(async () => {
      if (st.scenarioJson) applyScenario(JSON.parse(st.scenarioJson));
      setSection("e2e");
      await startE2E(true);
    }),
  );
  $("resumeDiscard").addEventListener(
    "click",
    guard(async () => {
      await api().DiscardE2EState();
      hideResumeBanner();
    }),
  );
}

async function startE2E(resume = false) {
  const p = currentProfile();
  if (!p) throw new Error("프로파일을 선택하세요.");
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

$("newProfile").addEventListener("click", () => fillForm(null));
bindBusy("saveProfile", async () => {
  const saved = await api().SaveProfile(profileFromForm());
  await loadProfiles(saved.id);
}, "저장 중...");
$("deleteProfile").addEventListener(
  "click",
  guard(async () => {
    if (!state.profileId) return;
    await api().DeleteProfile(state.profileId);
    await loadProfiles();
  }),
);
$("profileSelect").addEventListener("change", () => {
  const p = state.profiles.find((x) => x.id === $("profileSelect").value);
  if (p) fillForm(p);
});
bindBusy("exportProfiles", async (btn) => {
  const text = await api().ExportProfiles();
  await navigator.clipboard.writeText(text);
  $("discoverOut").textContent = "클립보드에 복사됨 (비밀번호는 placeholder 치환):\n\n" + text;
  flashButton(btn, "복사됨");
});
bindBusy("testMaria", async () => {
  const saved = await api().SaveProfile(profileFromForm());
  await loadProfiles(saved.id);
  const r = await api().TestMaria(saved.id);
  $("discoverOut").textContent = (r.ok ? "OK: " : "실패: ") + r.message;
}, "연결 확인 중...");
bindBusy("testCh", async () => {
  const saved = await api().SaveProfile(profileFromForm());
  await loadProfiles(saved.id);
  const r = await api().TestCH(saved.id);
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
  state.overrides.push({ hour: 12, mode: "goal", goal: { min: 20, max: 40, avg: 30 } });
  renderHourRows();
});
// input마다 재렌더하면 포커스가 소실됨 — 상태만 갱신, hour 라벨 셀만 부분 갱신
$("hourRows").addEventListener("input", (e) => {
  const i = Number(e.target.dataset.oi);
  const f = e.target.dataset.of;
  if (!Number.isInteger(i) || !f) return;
  if (f === "mode") state.overrides[i].mode = e.target.value;
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
    state.lastRunOk = !!res && !res.error && !res.canceled;
    updateInsertButton();
    updateStepStates();
    setProgress(1, 1);
    renderRunResult(res);
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
    const verdict = res.aborted ? `중단 (${res.aborted})` : res.pass ? "PASS" : "FAIL";
    $("e2eStatus").textContent = verdict;
    e2eLog(`E2E 종료: ${verdict} — verify ${res.verifyRuns}회`);
    if (res.finalVerify) renderVerify(res.finalVerify);
    e2eLog("리포트는 자동 저장되었고(로그의 경로 참조), 최종 verify 결과는 Verify 탭에 반영되었습니다.");
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

  updateE2EButtons();
  updateStepStates();
  bindRuntimeEvents();
  if (api()) {
    loadProfiles().catch(showError);
    checkPendingE2E().catch(() => {});
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
