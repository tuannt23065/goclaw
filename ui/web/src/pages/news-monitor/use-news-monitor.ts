import { useCallback, useEffect, useState } from "react";
import { useWs } from "@/hooks/use-ws";
import { toast } from "@/stores/use-toast-store";
import type { ApiError } from "@/api/errors";

export interface NewsFeed {
  id: string;
  url: string;
  sourceName: string;
  sourceType: "rss" | "atom" | "html" | "search";
  category: string;
  priority: number;
  active: boolean;
  fetchFailCount: number;
  lastPolledAt?: string;
}

export interface NewsItem {
  id: string;
  feedId: string;
  sourceName: string;
  url: string;
  title: string;
  summary: string;
  status: string;
  fetchedAt: string;
  publishedAt?: string;
  dispatchedAt?: string;
  heuristicScore?: number;
  dispatchedTaskId?: string;
  skipReason?: string;
}

export interface MonitorStatus {
  enabled: boolean;
  intervalMinutes: number;
  minDispatchGapMin: number;
  quietHoursStart: number;
  quietHoursEnd: number;
  heuristicThreshold: number;
  maxDispatchPerCycle: number;
  monitorTenantId: string;
  tenantMatchesMonitor: boolean;
  lastDispatchedAt?: string;
  last24h: {
    dispatched: number;
    scored: number;
    skipped: number;
    duplicates: number;
    totalFetched: number;
  };
}

interface FeedFormInput {
  url: string;
  sourceName: string;
  sourceType: NewsFeed["sourceType"];
  category?: string;
  priority?: number;
  active?: boolean;
}

export function useNewsMonitor() {
  const ws = useWs();
  const [feeds, setFeeds] = useState<NewsFeed[]>([]);
  const [items, setItems] = useState<NewsItem[]>([]);
  const [status, setStatus] = useState<MonitorStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const refreshFeeds = useCallback(async () => {
    const r = await ws.call<{ feeds: NewsFeed[] }>("news.feeds.list");
    setFeeds(r.feeds ?? []);
  }, [ws]);

  const refreshStatus = useCallback(async () => {
    const r = await ws.call<MonitorStatus>("news.monitor.status");
    setStatus(r);
  }, [ws]);

  const refreshItems = useCallback(
    async (filter: { status?: string; feedId?: string; sinceMin?: number; limit?: number } = {}) => {
      const r = await ws.call<{ items: NewsItem[] }>("news.items.list", {
        ...filter,
        limit: filter.limit ?? 50,
      });
      setItems(r.items ?? []);
    },
    [ws],
  );

  const refreshAll = useCallback(async () => {
    setRefreshing(true);
    try {
      await Promise.all([refreshFeeds(), refreshStatus(), refreshItems()]);
    } catch (e) {
      const err = e as ApiError;
      toast.error("Tải dữ liệu thất bại", err.message);
    } finally {
      setRefreshing(false);
      setLoading(false);
    }
  }, [refreshFeeds, refreshStatus, refreshItems]);

  useEffect(() => {
    void refreshAll();
  }, [refreshAll]);

  const createFeed = useCallback(
    async (input: FeedFormInput) => {
      try {
        await ws.call("news.feeds.create", input as unknown as Record<string, unknown>);
        toast.success("Đã thêm feed");
        await refreshFeeds();
        return true;
      } catch (e) {
        toast.error("Thêm feed thất bại", (e as ApiError).message);
        return false;
      }
    },
    [ws, refreshFeeds],
  );

  const updateFeed = useCallback(
    async (id: string, updates: Partial<FeedFormInput>) => {
      try {
        await ws.call("news.feeds.update", { id, ...updates } as Record<string, unknown>);
        toast.success("Đã cập nhật feed");
        await refreshFeeds();
        return true;
      } catch (e) {
        toast.error("Cập nhật feed thất bại", (e as ApiError).message);
        return false;
      }
    },
    [ws, refreshFeeds],
  );

  const toggleFeed = useCallback(
    async (id: string, active: boolean) => {
      try {
        await ws.call("news.feeds.toggle", { id, active });
        await refreshFeeds();
      } catch (e) {
        toast.error("Đổi trạng thái thất bại", (e as ApiError).message);
      }
    },
    [ws, refreshFeeds],
  );

  const deleteFeed = useCallback(
    async (id: string) => {
      try {
        await ws.call("news.feeds.delete", { id });
        toast.success("Đã xoá feed");
        await refreshFeeds();
      } catch (e) {
        toast.error("Xoá feed thất bại", (e as ApiError).message);
      }
    },
    [ws, refreshFeeds],
  );

  const triggerCycle = useCallback(async () => {
    try {
      await ws.call("news.monitor.run");
      toast.success(
        "Đã trigger cycle",
        "Cycle đang chạy nền. Refresh trong ~30-60s để xem items mới.",
      );
    } catch (e) {
      toast.error("Trigger thất bại", (e as ApiError).message);
    }
  }, [ws]);

  return {
    feeds,
    items,
    status,
    loading,
    refreshing,
    refreshAll,
    refreshItems,
    createFeed,
    updateFeed,
    toggleFeed,
    deleteFeed,
    triggerCycle,
  };
}
