import { useEffect, useMemo, useState } from "react";
import { Newspaper, Plus, RefreshCw, Play, Trash2, Edit3, ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { PageHeader } from "@/components/shared/page-header";
import { EmptyState } from "@/components/shared/empty-state";
import { ConfirmDialog } from "@/components/shared/confirm-dialog";
import { TableSkeleton } from "@/components/shared/loading-skeleton";
import { useMinLoading } from "@/hooks/use-min-loading";
import { useNewsMonitor, type NewsFeed } from "./use-news-monitor";

export function NewsMonitorPage() {
  const m = useNewsMonitor();
  const spinning = useMinLoading(m.refreshing);

  const [tab, setTab] = useState<"feeds" | "items" | "settings">("feeds");
  const [showCreate, setShowCreate] = useState(false);
  const [editing, setEditing] = useState<NewsFeed | null>(null);
  const [deleting, setDeleting] = useState<NewsFeed | null>(null);

  if (m.loading) {
    return (
      <div className="p-4 sm:p-6">
        <PageHeader title="News Monitor" description="Quản lý feeds + items + cycle status" />
        <TableSkeleton rows={5} />
      </div>
    );
  }

  return (
    <div className="p-4 sm:p-6 pb-10">
      <PageHeader
        title="News Monitor"
        description="RSS/HTML/search poller dispatch tin lên goctech leader. Quản lý feeds + items + cycle."
        actions={
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => void m.refreshAll()} disabled={spinning}>
              <RefreshCw className={`h-4 w-4 ${spinning ? "animate-spin" : ""}`} />
              <span className="ml-1 hidden sm:inline">Refresh</span>
            </Button>
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="h-4 w-4" />
              <span className="ml-1">Thêm feed</span>
            </Button>
          </div>
        }
      />

      {m.status && !m.status.enabled && (
        <Card className="mb-4 border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          News monitor hiện đang <strong>tắt</strong> trong config.json. Feeds vẫn quản lý được nhưng cycle sẽ không chạy. Đặt
          <code className="mx-1 rounded bg-amber-200/40 px-1">news_monitor.enabled = true</code>và restart container để bật.
        </Card>
      )}
      {m.status && m.status.enabled && !m.status.tenantMatchesMonitor && (
        <Card className="mb-4 border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:bg-amber-950/40 dark:text-amber-200">
          Monitor đang chạy cho tenant <code>{m.status.monitorTenantId}</code>, không phải tenant hiện tại của bạn. Feeds bạn thêm sẽ
          KHÔNG được monitor pick lên cho tới khi có per-tenant monitor instance (Phase 3.5).
        </Card>
      )}

      <Tabs value={tab} onValueChange={(v) => setTab(v as "feeds" | "items" | "settings")}>
        <TabsList>
          <TabsTrigger value="feeds">Feeds ({m.feeds.length})</TabsTrigger>
          <TabsTrigger value="items">Items</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="feeds" className="mt-4">
          <FeedsTab
            feeds={m.feeds}
            onToggle={(id, active) => void m.toggleFeed(id, active)}
            onEdit={setEditing}
            onDelete={setDeleting}
          />
        </TabsContent>

        <TabsContent value="items" className="mt-4">
          <ItemsTab
            items={m.items}
            feeds={m.feeds}
            refresh={m.refreshItems}
          />
        </TabsContent>

        <TabsContent value="settings" className="mt-4">
          <SettingsTab status={m.status} onTrigger={() => void m.triggerCycle()} />
        </TabsContent>
      </Tabs>

      <FeedFormDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        onSubmit={async (input) => {
          const ok = await m.createFeed(input);
          if (ok) setShowCreate(false);
        }}
      />
      <FeedFormDialog
        open={!!editing}
        feed={editing ?? undefined}
        onClose={() => setEditing(null)}
        onSubmit={async (input) => {
          if (!editing) return;
          const ok = await m.updateFeed(editing.id, input);
          if (ok) setEditing(null);
        }}
      />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Xoá feed?"
        description={`Xoá "${deleting?.sourceName}" (${deleting?.url}) — TẤT CẢ items đã fetch sẽ bị xoá theo (CASCADE). Không thể hoàn tác.`}
        confirmLabel="Xoá vĩnh viễn"
        variant="destructive"
        onConfirm={async () => {
          if (deleting) await m.deleteFeed(deleting.id);
          setDeleting(null);
        }}
      />
    </div>
  );
}

// -----------------------------------------------------------------------------
// Tab: Feeds
// -----------------------------------------------------------------------------

function FeedsTab({
  feeds,
  onToggle,
  onEdit,
  onDelete,
}: {
  feeds: NewsFeed[];
  onToggle: (id: string, active: boolean) => void;
  onEdit: (f: NewsFeed) => void;
  onDelete: (f: NewsFeed) => void;
}) {
  if (feeds.length === 0) {
    return <EmptyState icon={Newspaper} title="Chưa có feed nào" description="Bấm 'Thêm feed' để bắt đầu." />;
  }
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="min-w-[800px] w-full text-sm">
        <thead className="bg-muted/50 text-left">
          <tr>
            <th className="p-2 w-16">Active</th>
            <th className="p-2">Source</th>
            <th className="p-2">URL</th>
            <th className="p-2">Type</th>
            <th className="p-2">Category</th>
            <th className="p-2 text-right">Pri</th>
            <th className="p-2">Last poll</th>
            <th className="p-2 text-right">Fail</th>
            <th className="p-2"></th>
          </tr>
        </thead>
        <tbody>
          {feeds.map((f) => (
            <tr key={f.id} className="border-t">
              <td className="p-2">
                <Switch checked={f.active} onCheckedChange={(v) => onToggle(f.id, v)} />
              </td>
              <td className="p-2 font-medium">{f.sourceName}</td>
              <td className="p-2">
                <a href={f.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
                  {truncate(f.url, 40)}
                  <ExternalLink className="h-3 w-3" />
                </a>
              </td>
              <td className="p-2">
                <Badge variant={f.sourceType === "search" ? "default" : "secondary"}>{f.sourceType}</Badge>
              </td>
              <td className="p-2 text-muted-foreground">{f.category || "—"}</td>
              <td className="p-2 text-right">{f.priority}</td>
              <td className="p-2 text-xs text-muted-foreground">{f.lastPolledAt ? formatRelative(f.lastPolledAt) : "—"}</td>
              <td className="p-2 text-right">
                {f.fetchFailCount > 0 ? <span className="text-destructive">{f.fetchFailCount}</span> : "0"}
              </td>
              <td className="p-2 text-right">
                <Button variant="ghost" size="sm" onClick={() => onEdit(f)}>
                  <Edit3 className="h-3.5 w-3.5" />
                </Button>
                <Button variant="ghost" size="sm" onClick={() => onDelete(f)}>
                  <Trash2 className="h-3.5 w-3.5 text-destructive" />
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// -----------------------------------------------------------------------------
// Tab: Items
// -----------------------------------------------------------------------------

function ItemsTab({
  items,
  feeds,
  refresh,
}: {
  items: ReturnType<typeof useNewsMonitor>["items"];
  feeds: NewsFeed[];
  refresh: (filter?: { status?: string; feedId?: string; sinceMin?: number; limit?: number }) => Promise<void>;
}) {
  const [statusFilter, setStatusFilter] = useState<string>("all");
  const [feedFilter, setFeedFilter] = useState<string>("all");

  useEffect(() => {
    const f: { status?: string; feedId?: string } = {};
    if (statusFilter !== "all") f.status = statusFilter;
    if (feedFilter !== "all") f.feedId = feedFilter;
    void refresh(f);
  }, [statusFilter, feedFilter, refresh]);

  const feedNameById = useMemo(() => {
    const m: Record<string, string> = {};
    feeds.forEach((f) => (m[f.id] = f.sourceName));
    return m;
  }, [feeds]);

  return (
    <div>
      <div className="mb-3 flex flex-wrap gap-2">
        <Select value={statusFilter} onValueChange={setStatusFilter}>
          <SelectTrigger className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">Tất cả status</SelectItem>
            <SelectItem value="dispatched">Dispatched</SelectItem>
            <SelectItem value="scored">Scored</SelectItem>
            <SelectItem value="skipped">Skipped</SelectItem>
            <SelectItem value="duplicate">Duplicate</SelectItem>
            <SelectItem value="new">New</SelectItem>
          </SelectContent>
        </Select>
        <Select value={feedFilter} onValueChange={setFeedFilter}>
          <SelectTrigger className="w-48">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">Tất cả feed</SelectItem>
            {feeds.map((f) => (
              <SelectItem key={f.id} value={f.id}>
                {f.sourceName}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {items.length === 0 ? (
        <EmptyState icon={Newspaper} title="Không có items" description="Thử bỏ filter hoặc đợi cycle tiếp theo." />
      ) : (
        <div className="overflow-x-auto rounded-md border">
          <table className="min-w-[700px] w-full text-sm">
            <thead className="bg-muted/50 text-left">
              <tr>
                <th className="p-2">Status</th>
                <th className="p-2 w-16 text-right">Score</th>
                <th className="p-2">Title</th>
                <th className="p-2">Source</th>
                <th className="p-2">Fetched</th>
              </tr>
            </thead>
            <tbody>
              {items.map((it) => (
                <tr key={it.id} className="border-t align-top">
                  <td className="p-2">
                    <StatusBadge status={it.status} />
                  </td>
                  <td className="p-2 text-right">{it.heuristicScore ?? "—"}</td>
                  <td className="p-2">
                    <a href={it.url} target="_blank" rel="noreferrer" className="font-medium hover:underline">
                      {it.title}
                    </a>
                    {it.summary && <div className="text-xs text-muted-foreground line-clamp-2 mt-0.5">{it.summary}</div>}
                    {it.skipReason && <div className="text-xs text-amber-600 mt-0.5">↳ {it.skipReason}</div>}
                  </td>
                  <td className="p-2 text-xs">{it.sourceName || feedNameById[it.feedId] || "—"}</td>
                  <td className="p-2 text-xs text-muted-foreground whitespace-nowrap">{formatRelative(it.fetchedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// -----------------------------------------------------------------------------
// Tab: Settings (read-only + trigger button)
// -----------------------------------------------------------------------------

function SettingsTab({
  status,
  onTrigger,
}: {
  status: ReturnType<typeof useNewsMonitor>["status"];
  onTrigger: () => void;
}) {
  if (!status) return null;
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Card className="p-4">
        <div className="text-sm font-medium mb-3">Cấu hình hiện tại (config.json — read-only)</div>
        <dl className="grid grid-cols-2 gap-y-2 text-sm">
          <dt className="text-muted-foreground">Enabled</dt>
          <dd>{status.enabled ? <Badge variant="default">on</Badge> : <Badge variant="secondary">off</Badge>}</dd>
          <dt className="text-muted-foreground">Cycle interval</dt>
          <dd>{status.intervalMinutes} phút</dd>
          <dt className="text-muted-foreground">Min dispatch gap</dt>
          <dd>{status.minDispatchGapMin} phút</dd>
          <dt className="text-muted-foreground">Quiet hours (VN)</dt>
          <dd>{status.quietHoursStart}h–{status.quietHoursEnd}h</dd>
          <dt className="text-muted-foreground">Heuristic threshold</dt>
          <dd>{status.heuristicThreshold}</dd>
          <dt className="text-muted-foreground">Max dispatch / cycle</dt>
          <dd>{status.maxDispatchPerCycle}</dd>
        </dl>
        <div className="text-xs text-muted-foreground mt-3 pt-3 border-t">
          Đổi config: sửa <code>news_monitor.*</code> trong <code>/app/data/config.json</code> + restart container.
        </div>
      </Card>

      <Card className="p-4">
        <div className="text-sm font-medium mb-3">Last 24 giờ</div>
        <dl className="grid grid-cols-2 gap-y-2 text-sm">
          <dt className="text-muted-foreground">Items đã fetch</dt>
          <dd>{status.last24h.totalFetched}</dd>
          <dt className="text-muted-foreground">Đã scored</dt>
          <dd>{status.last24h.scored}</dd>
          <dt className="text-muted-foreground">Đã dispatch</dt>
          <dd className="font-medium">{status.last24h.dispatched}</dd>
          <dt className="text-muted-foreground">Skipped</dt>
          <dd>{status.last24h.skipped}</dd>
          <dt className="text-muted-foreground">Duplicate</dt>
          <dd>{status.last24h.duplicates}</dd>
          <dt className="text-muted-foreground">Last dispatch</dt>
          <dd className="text-xs">{status.lastDispatchedAt ? formatRelative(status.lastDispatchedAt) : "—"}</dd>
        </dl>
        <div className="mt-4 pt-3 border-t">
          <Button size="sm" onClick={onTrigger} disabled={!status.enabled}>
            <Play className="h-4 w-4 mr-1" />
            Trigger cycle ngay
          </Button>
          <div className="text-xs text-muted-foreground mt-2">
            Rate-limited: 1 lần/phút/tenant. Cycle chạy nền 30-180s tùy số feeds search.
          </div>
        </div>
      </Card>
    </div>
  );
}

// -----------------------------------------------------------------------------
// Feed form dialog (create + edit)
// -----------------------------------------------------------------------------

function FeedFormDialog({
  open,
  feed,
  onClose,
  onSubmit,
}: {
  open: boolean;
  feed?: NewsFeed;
  onClose: () => void;
  onSubmit: (input: {
    url: string;
    sourceName: string;
    sourceType: NewsFeed["sourceType"];
    category?: string;
    priority?: number;
    active?: boolean;
  }) => Promise<void>;
}) {
  const [url, setUrl] = useState("");
  const [sourceName, setSourceName] = useState("");
  const [sourceType, setSourceType] = useState<NewsFeed["sourceType"]>("rss");
  const [category, setCategory] = useState("");
  const [priority, setPriority] = useState(5);
  const [active, setActive] = useState(true);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (open) {
      setUrl(feed?.url ?? "");
      setSourceName(feed?.sourceName ?? "");
      setSourceType(feed?.sourceType ?? "rss");
      setCategory(feed?.category ?? "");
      setPriority(feed?.priority ?? 5);
      setActive(feed?.active ?? true);
    }
  }, [open, feed]);

  const handleSubmit = async () => {
    if (!url.trim() || !sourceName.trim()) return;
    setSubmitting(true);
    try {
      await onSubmit({
        url: url.trim(),
        sourceName: sourceName.trim(),
        sourceType,
        category: category.trim() || undefined,
        priority,
        active,
      });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{feed ? "Sửa feed" : "Thêm feed mới"}</DialogTitle>
        </DialogHeader>
        <div className="space-y-3">
          <div>
            <Label htmlFor="feed-name">Source name *</Label>
            <Input id="feed-name" value={sourceName} onChange={(e) => setSourceName(e.target.value)} placeholder="VD: TechCrunch" />
          </div>
          <div>
            <Label htmlFor="feed-url">URL *</Label>
            <Input id="feed-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://..." />
            {sourceType === "search" && (
              <div className="text-xs text-amber-600 mt-1">
                Search type cần URL là bare hostname (vd <code>https://openai.com</code>), KHÔNG có path.
              </div>
            )}
          </div>
          <div>
            <Label>Source type</Label>
            <Select value={sourceType} onValueChange={(v) => setSourceType(v as NewsFeed["sourceType"])}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="rss">RSS / Atom feed</SelectItem>
                <SelectItem value="atom">Atom (alias)</SelectItem>
                <SelectItem value="html">HTML scrape (gofeed/goquery)</SelectItem>
                <SelectItem value="search">Search (claude -p + WebSearch — cho CF-gated sites)</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label htmlFor="feed-category">Category</Label>
              <Input id="feed-category" value={category} onChange={(e) => setCategory(e.target.value)} placeholder="ai / chip / security..." />
            </div>
            <div>
              <Label htmlFor="feed-priority">Priority (1-10)</Label>
              <Input
                id="feed-priority"
                type="number"
                min={1}
                max={10}
                value={priority}
                onChange={(e) => setPriority(parseInt(e.target.value, 10) || 5)}
              />
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="feed-active" checked={active} onCheckedChange={setActive} />
            <Label htmlFor="feed-active">Active</Label>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={submitting}>
            Huỷ
          </Button>
          <Button onClick={handleSubmit} disabled={submitting || !url.trim() || !sourceName.trim()}>
            {submitting ? "Đang lưu..." : feed ? "Lưu" : "Thêm"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

function StatusBadge({ status }: { status: string }) {
  const variant: Record<string, "default" | "secondary" | "destructive" | "outline"> = {
    dispatched: "default",
    scored: "outline",
    skipped: "secondary",
    duplicate: "secondary",
    new: "outline",
    error: "destructive",
  };
  return <Badge variant={variant[status] ?? "outline"}>{status}</Badge>;
}

function truncate(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n - 1) + "…";
}

function formatRelative(iso: string): string {
  const t = new Date(iso).getTime();
  const diff = Date.now() - t;
  const sec = Math.round(diff / 1000);
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const h = Math.round(min / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  return `${d}d ago`;
}
