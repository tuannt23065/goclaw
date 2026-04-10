import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Link2, ChevronLeft, ChevronRight,
  FileText, Brain, StickyNote, Sparkles, Clock, Image,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatRelativeTime } from "@/lib/format";
import type { VaultDocument } from "@/types/vault";

const DOC_TYPE_CONFIG: Record<string, { color: string; bg: string; icon: typeof FileText }> = {
  context: { color: "text-blue-600 dark:text-blue-400", bg: "bg-blue-500/10", icon: FileText },
  memory: { color: "text-purple-600 dark:text-purple-400", bg: "bg-purple-500/10", icon: Brain },
  note: { color: "text-amber-600 dark:text-amber-400", bg: "bg-amber-500/10", icon: StickyNote },
  skill: { color: "text-emerald-600 dark:text-emerald-400", bg: "bg-emerald-500/10", icon: Sparkles },
  episodic: { color: "text-orange-600 dark:text-orange-400", bg: "bg-orange-500/10", icon: Clock },
  media: { color: "text-rose-600 dark:text-rose-400", bg: "bg-rose-500/10", icon: Image },
};

const DEFAULT_CONFIG = { color: "text-muted-foreground", bg: "bg-muted", icon: FileText };

interface Props {
  documents: VaultDocument[];
  selectedId: string | null;
  linkCounts: Map<string, number>;
  onSelect: (doc: VaultDocument) => void;
  loading: boolean;
  page: number;
  totalPages: number;
  total: number;
  onPageChange: (page: number) => void;
}

function DocCard({ doc, selected, linkCount, onClick }: {
  doc: VaultDocument; selected: boolean; linkCount: number; onClick: () => void;
}) {
  const { t } = useTranslation("vault");
  const ref = useRef<HTMLDivElement>(null);
  const cfg = DOC_TYPE_CONFIG[doc.doc_type] ?? DEFAULT_CONFIG;
  const Icon = cfg.icon;

  useEffect(() => {
    if (selected) ref.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [selected]);

  return (
    <div
      ref={ref}
      className={`group mx-1.5 my-0.5 flex items-center gap-2 rounded-md px-2 py-1.5 cursor-pointer transition-all ${
        selected
          ? "bg-accent shadow-sm ring-1 ring-accent-foreground/10"
          : "hover:bg-muted/60"
      }`}
      onClick={onClick}
    >
      {/* Type icon */}
      <div className={`flex h-6 w-6 shrink-0 items-center justify-center rounded ${cfg.bg}`}>
        <Icon className={`h-3 w-3 ${cfg.color}`} />
      </div>

      {/* Content */}
      <div className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium leading-snug">
          {doc.title || doc.path.split("/").pop()}
        </span>

        <div className="mt-0.5 flex items-center gap-1.5 text-2xs text-muted-foreground">
          <span>{t(`type.${doc.doc_type}`)}</span>
          <span>·</span>
          <span>{t(`scope.${doc.scope}`)}</span>
          {linkCount > 0 && (
            <>
              <span>·</span>
              <span className="flex items-center gap-0.5">
                <Link2 className="h-2.5 w-2.5" />
                {linkCount}
              </span>
            </>
          )}
          <span>·</span>
          <span>{formatRelativeTime(doc.updated_at)}</span>
        </div>
      </div>
    </div>
  );
}

export function VaultDocumentSidebar({
  documents, selectedId, linkCounts, onSelect, loading, page, totalPages, total, onPageChange,
}: Props) {
  const { t } = useTranslation("vault");

  return (
    <div className="flex h-full flex-col border-r bg-background">
      {/* Header */}
      <div className="flex h-10 items-center justify-between px-3 border-b shrink-0">
        <span className="text-sm font-semibold">{t("title")}</span>
        <Badge variant="secondary" className="text-2xs tabular-nums">{total}</Badge>
      </div>

      {/* Doc list */}
      <div className="flex-1 overflow-y-auto py-1">
        {loading && documents.length === 0 ? (
          Array.from({ length: 6 }).map((_, i) => (
            <div key={i} className="mx-1.5 my-0.5 flex items-center gap-2 rounded-md px-2 py-1.5">
              <div className="h-6 w-6 shrink-0 animate-pulse rounded bg-muted" />
              <div className="flex-1 space-y-1">
                <div className="h-3.5 w-3/4 animate-pulse rounded bg-muted" />
                <div className="h-2.5 w-1/2 animate-pulse rounded bg-muted" />
              </div>
            </div>
          ))
        ) : documents.length === 0 ? (
          <div className="flex flex-col items-center justify-center h-32 gap-1 text-muted-foreground">
            <FileText className="h-5 w-5" />
            <span className="text-sm">{t("noDocuments")}</span>
          </div>
        ) : (
          documents.map((doc) => (
            <DocCard
              key={doc.id}
              doc={doc}
              selected={doc.id === selectedId}
              linkCount={linkCounts.get(doc.id) ?? 0}
              onClick={() => onSelect(doc)}
            />
          ))
        )}
      </div>

      {/* Pagination footer */}
      {totalPages > 1 && (
        <div className="flex items-center justify-center gap-2 px-3 py-1.5 border-t text-xs text-muted-foreground">
          <Button variant="ghost" size="xs" disabled={page === 0} onClick={() => onPageChange(page - 1)}>
            <ChevronLeft className="h-3.5 w-3.5" />
          </Button>
          <span className="tabular-nums">{page + 1} / {totalPages}</span>
          <Button variant="ghost" size="xs" disabled={page >= totalPages - 1} onClick={() => onPageChange(page + 1)}>
            <ChevronRight className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}
    </div>
  );
}
