# Source: browser_use/tokens/service.py#L48-L250 (excerpts)
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s10.
#
# Three excerpts: (1) class header, (2) on-disk cache lifecycle,
# (3) calculate_cost + add_usage math.
#
# Orientation: include_cost gates cheap-path (count only) vs full-path
# ($-math). Our Go always computes — pricing lookup is a map access.
# _pricing_data is lazy-loaded; our Go init() parses the embedded JSON
# eagerly so failures surface at startup. calculate_cost has three
# input-token buckets (new / cached-read / cache-creation); our Go has
# one. Anthropic prompt caching = deliberate omission per README.


# ── (1) Class header — the fields that define a TokenCost instance ──

class TokenCost:
    """Service for tracking token usage and calculating costs"""

    CACHE_DIR_NAME = 'browser_use/token_cost'
    CACHE_DURATION = timedelta(days=1)
    DEFAULT_PRICING_URL = 'https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json'

    def __init__(self, include_cost: bool = False, pricing_url: str | None = None):
        # Feature flag: cheap path vs full $-computing path.
        self.include_cost = include_cost or os.getenv('BROWSER_USE_CALCULATE_COST', 'false').lower() == 'true'
        self.pricing_url = pricing_url or CONFIG.BROWSER_USE_MODEL_PRICING_URL or self.DEFAULT_PRICING_URL

        # In-memory ledger; maps to our Go TokenCost.History []Usage.
        self.usage_history: list[TokenUsageEntry] = []

        # LLMs whose .ainvoke we've patched. Our Go skips the
        # monkey-patch — callers explicitly call RegisterInvocation.
        self.registered_llms: dict[str, BaseChatModel] = {}

        # Pricing is lazy-loaded; our Go init() does it eager.
        self._pricing_data: dict[str, Any] | None = None
        self._cache_dir = xdg_cache_home() / self.CACHE_DIR_NAME


# ── (2) Disk-cache lifecycle — our Refresher.fresh() is the in-memory mirror ──

    async def _load_pricing_data(self) -> None:
        """Load pricing data from cache or fetch from GitHub"""
        cache_file = await self._find_valid_cache()
        if cache_file:
            await self._load_from_cache(cache_file)
        else:
            await self._fetch_and_cache_pricing_data()

    async def _find_valid_cache(self) -> Path | None:
        """Find the most recent valid cache file"""
        try:
            self._cache_dir.mkdir(parents=True, exist_ok=True)
            cache_files = list(self._cache_dir.glob('*.json'))
            if not cache_files:
                return None
            # Most recent first → check first.
            cache_files.sort(key=lambda f: f.stat().st_mtime, reverse=True)
            for cache_file in cache_files:
                is_valid, should_delete = await self._get_cache_status(cache_file)
                if is_valid:
                    return cache_file
                if should_delete:
                    try:
                        os.remove(cache_file)  # GC stale files
                    except Exception:
                        pass
            return None
        except Exception:
            return None


# ── (3) The math — our RegisterInvocation mirrors this, minus the cache buckets ──

    async def calculate_cost(self, model: str, usage: ChatInvokeUsage) -> TokenCostCalculated | None:
        if not self.include_cost:
            return None
        data = await self.get_model_pricing(model)
        if data is None:
            return None  # ← unknown model = None, not raise

        # New = total prompt tokens minus tokens from Anthropic cache.
        # Cached tokens are charged at a cheaper rate (cache_read).
        uncached_prompt_tokens = usage.prompt_tokens - (usage.prompt_cached_tokens or 0)
        return TokenCostCalculated(
            new_prompt_tokens=usage.prompt_tokens,
            new_prompt_cost=uncached_prompt_tokens * (data.input_cost_per_token or 0),
            prompt_read_cached_tokens=usage.prompt_cached_tokens,
            prompt_read_cached_cost=(usage.prompt_cached_tokens * data.cache_read_input_token_cost)
            if usage.prompt_cached_tokens and data.cache_read_input_token_cost else None,
            prompt_cached_creation_tokens=usage.prompt_cache_creation_tokens,
            prompt_cache_creation_cost=(usage.prompt_cache_creation_tokens * data.cache_creation_input_token_cost)
            if data.cache_creation_input_token_cost and usage.prompt_cache_creation_tokens else None,
            completion_tokens=usage.completion_tokens,
            completion_cost=usage.completion_tokens * float(data.output_cost_per_token or 0),
        )

    def add_usage(self, model: str, usage: ChatInvokeUsage) -> TokenUsageEntry:
        """Add token usage entry to history (without calculating cost)"""
        # Upstream separates add_usage and calculate_cost so cost
        # math is opt-in. Our Go RegisterInvocation collapses both —
        # the pricing lookup is nanoseconds, no point splitting.
        entry = TokenUsageEntry(model=model, timestamp=datetime.now(), usage=usage)
        self.usage_history.append(entry)
        return entry
