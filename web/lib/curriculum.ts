// The locked curriculum from the plan. SessionNav and the landing page both
// read from this single source of truth. Slugs match docs/{zh,en}/<slug>.md.
//
// "available: false" means the chapter exists in the curriculum but its
// docs aren't written yet — the link will render but go to a placeholder.

export type ChapterMeta = {
  slug: string;
  num: string; // "s01", "s02", "s_full"
  title: { zh: string; en: string };
  available: boolean;
};

export const CURRICULUM: ChapterMeta[] = [
  {
    slug: "multi-model",
    num: "M",
    title: {
      zh: "多模型接入指南（DeepSeek / Qwen / 自托管 …）",
      en: "Multi-model guide (DeepSeek / Qwen / self-hosted …)",
    },
    available: false,
  },
  {
    slug: "s01-minimum-loop",
    num: "s01",
    title: { zh: "最小 agent 循环", en: "Minimum agent loop" },
    available: true,
  },
  {
    slug: "s02-llm-provider",
    num: "s02",
    title: { zh: "LLM Provider 抽象", en: "LLM Provider abstraction" },
    available: true,
  },
  {
    slug: "s03-message-manager",
    num: "s03",
    title: { zh: "消息管理与压缩", en: "Message manager + compaction" },
    available: true,
  },
  {
    slug: "s04-tool-registry",
    num: "s04",
    title: { zh: "工具注册表", en: "Tool registry & dispatcher" },
    available: true,
  },
  {
    slug: "s05-element-actor",
    num: "s05",
    title: { zh: "元素操作 (CDP 抽象)", en: "Element actor (CDP abstraction)" },
    available: true,
  },
  {
    slug: "s06-watchdog-pattern",
    num: "s06",
    title: { zh: "看门狗事件总线", en: "Watchdog & event bus" },
    available: true,
  },
  {
    slug: "s07-browser-session",
    num: "s07",
    title: { zh: "浏览器会话", en: "Browser session" },
    available: true,
  },
  {
    slug: "s08-dom-serializer",
    num: "s08",
    title: { zh: "DOM 序列化", en: "DOM serializer for LLM" },
    available: false,
  },
  {
    slug: "s09-dom-service",
    num: "s09",
    title: { zh: "DOM 服务", en: "DOM service (snapshot + filter)" },
    available: false,
  },
  {
    slug: "s10-token-cost",
    num: "s10",
    title: { zh: "Token 计费", en: "Token cost tracking" },
    available: false,
  },
  {
    slug: "s11-filesystem-sandbox",
    num: "s11",
    title: { zh: "文件系统沙箱", en: "Filesystem sandbox" },
    available: false,
  },
  {
    slug: "s12-agent-loop-full",
    num: "s12",
    title: { zh: "完整 agent loop", en: "Full agent loop (integration code)" },
    available: false,
  },
  {
    slug: "s_full-integration",
    num: "s_full",
    title: { zh: "端到端集成", en: "End-to-end integration" },
    available: false,
  },
  {
    slug: "appendix-a-llm-as-driver",
    num: "A",
    title: {
      zh: "附录 A · LLM-as-driver 哲学",
      en: "Appendix A · LLM-as-driver philosophy",
    },
    available: false,
  },
  {
    slug: "appendix-b-upstream-map",
    num: "B",
    title: {
      zh: "附录 B · 上游源码导读地图",
      en: "Appendix B · Upstream source-reading map",
    },
    available: false,
  },
];

export type Locale = "zh" | "en";

export function chapterTitle(c: ChapterMeta, locale: Locale): string {
  return c.title[locale];
}
