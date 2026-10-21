import {
	closestCenter,
	DndContext,
	type DragEndEvent,
	KeyboardSensor,
	type Modifier,
	PointerSensor,
	useDndContext,
	useSensor,
	useSensors,
} from "@dnd-kit/core"
import {
	rectSortingStrategy,
	SortableContext,
	sortableKeyboardCoordinates,
	useSortable,
	verticalListSortingStrategy,
} from "@dnd-kit/sortable"
import { CSS } from "@dnd-kit/utilities"
import { useLingui as useLinguiContext } from "@lingui/react"
import { Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { subscribeKeys } from "nanostores"
import { getPagePath } from "@nanostores/router"
import {
	type ColumnDef,
	type ColumnFiltersState,
	flexRender,
	getCoreRowModel,
	getFilteredRowModel,
	getSortedRowModel,
	type Row,
	type SortingState,
	type Table as TableType,
	useReactTable,
	type VisibilityState,
} from "@tanstack/react-table"
import { defaultRangeExtractor, type Range, useVirtualizer, type VirtualItem } from "@tanstack/react-virtual"
import {
	ArrowDownIcon,
	ArrowUpDownIcon,
	ArrowUpIcon,
	EyeIcon,
	FilterIcon,
	GripVerticalIcon,
	LayoutGridIcon,
	LayoutListIcon,
	LoaderCircleIcon,
	Settings2Icon,
	XIcon,
} from "lucide-react"
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { SystemStatus } from "@/lib/enums"
import { queueUserSettings } from "@/lib/api"
import { $downSystems, $pausedSystems, $systems, $systemsLoaded, $upSystems, $userSettings } from "@/lib/stores"
import { moveSystem, orderSystems, seedOrder } from "@/lib/system-order"
import { cn, runOnce } from "@/lib/utils"
import type { SystemRecord } from "@/types"
import AlertButton from "../alerts/alert-button"
import { $router, Link } from "../router"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../ui/card"
import { SystemsTableColumns, ActionsButton, IndicatorDot } from "./systems-table-columns"

type ViewMode = "table" | "grid"
type StatusFilter = "all" | SystemRecord["status"]

const preloadSystemDetail = runOnce(() => import("@/components/routes/system.tsx"))

const restrictToVerticalAxis: Modifier = ({ transform }) => ({ ...transform, x: 0 })
// a dragged item past the end of its list grows the scroll area, and auto-scroll then chases it forever
const restrictToList: Modifier = ({ containerNodeRect, draggingNodeRect, transform }) => {
	if (!containerNodeRect || !draggingNodeRect) return transform
	return {
		...transform,
		x: Math.min(
			Math.max(transform.x, containerNodeRect.left - draggingNodeRect.left),
			containerNodeRect.right - draggingNodeRect.right
		),
		y: Math.min(
			Math.max(transform.y, containerNodeRect.top - draggingNodeRect.top),
			containerNodeRect.bottom - draggingNodeRect.bottom
		),
	}
}
const tableModifiers = [restrictToVerticalAxis, restrictToList]
const gridModifiers = [restrictToList]
// useSensor memoizes on the options object, so inline literals would give every row new listeners each render
const pointerSensorOptions = { activationConstraint: { distance: 4 } }
const keyboardSensorOptions = { coordinateGetter: sortableKeyboardCoordinates }
// transform stays out of the card's hover transition, or the card animates its leftover drag offset after a drop
const cardTransition = "box-shadow 200ms, opacity 200ms"

export default function SystemsTable() {
	const data = useStore($systems)
	const systemsLoaded = useStore($systemsLoaded)
	const downSystems = $downSystems.get()
	const upSystems = $upSystems.get()
	const pausedSystems = $pausedSystems.get()
	const { i18n, t } = useLingui()
	const [filter, setFilter] = useState<string>("")
	const [statusFilter, setStatusFilter] = useState<StatusFilter>(
		() =>
			$userSettings.get().statusFilter ??
			(JSON.parse(localStorage.getItem("besz-statusFilter") || "null") as StatusFilter | null) ??
			"all"
	)
	const [sorting, setSorting] = useState<SortingState>(
		() =>
			$userSettings.get().sortMode ??
			JSON.parse(sessionStorage.getItem("besz-sortMode") || "null") ?? [{ id: "system", desc: false }]
	)
	const [systemOrder, setSystemOrder] = useState<string[]>(
		() => $userSettings.get().systemOrder ?? JSON.parse(localStorage.getItem("besz-systemOrder") || "[]")
	)
	const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
	const [columnVisibility, setColumnVisibility] = useState<VisibilityState>(
		() => $userSettings.get().cols ?? JSON.parse(localStorage.getItem("besz-cols") || "{}")
	)

	// Apply settings from server once they load (handles incognito / new devices)
	const applied = useRef(new Set<string>())
	useEffect(() => {
		return subscribeKeys($userSettings, ["cols", "statusFilter", "viewMode", "sortMode", "systemOrder"], (vals) => {
			if (!applied.current.has("cols") && vals.cols !== undefined) {
				applied.current.add("cols")
				setColumnVisibility(vals.cols)
			}
			if (!applied.current.has("statusFilter") && vals.statusFilter !== undefined) {
				applied.current.add("statusFilter")
				setStatusFilter(vals.statusFilter)
			}
			if (!applied.current.has("viewMode") && vals.viewMode !== undefined) {
				applied.current.add("viewMode")
				setViewMode(vals.viewMode)
			}
			if (!applied.current.has("sortMode") && vals.sortMode !== undefined) {
				applied.current.add("sortMode")
				setSorting(vals.sortMode)
			}
			if (!applied.current.has("systemOrder") && vals.systemOrder !== undefined) {
				applied.current.add("systemOrder")
				setSystemOrder(vals.systemOrder)
			}
		})
	}, [])

	const handleColumnVisibilityChange = useCallback(
		(updater: VisibilityState | ((prev: VisibilityState) => VisibilityState)) => {
			setColumnVisibility((prev) => {
				const next = typeof updater === "function" ? updater(prev) : updater
				localStorage.setItem("besz-cols", JSON.stringify(next))
				$userSettings.setKey("cols", next)
				queueUserSettings({ cols: next })
				return next
			})
		},
		[]
	)

	const handleStatusFilterChange = useCallback((value: string) => {
		const next = value as StatusFilter
		setStatusFilter(next)
		localStorage.setItem("besz-statusFilter", JSON.stringify(next))
		$userSettings.setKey("statusFilter", next)
		queueUserSettings({ statusFilter: next })
	}, [])

	const handleViewModeChange = useCallback((view: string) => {
		const next = view as ViewMode
		setViewMode(next)
		localStorage.setItem("besz-viewMode", JSON.stringify(next))
		$userSettings.setKey("viewMode", next)
		queueUserSettings({ viewMode: next })
	}, [])

	const handleSortingChange = useCallback((updater: SortingState | ((prev: SortingState) => SortingState)) => {
		setSorting((prev) => {
			const next = typeof updater === "function" ? updater(prev) : updater
			sessionStorage.setItem("besz-sortMode", JSON.stringify(next))
			$userSettings.setKey("sortMode", next)
			queueUserSettings({ sortMode: next })
			return next
		})
	}, [])

	const locale = i18n.locale

	// Filter data based on status filter
	const filteredData = useMemo(() => {
		if (statusFilter === "all") {
			return data
		}
		if (statusFilter === SystemStatus.Up) {
			return Object.values(upSystems) ?? []
		}
		if (statusFilter === SystemStatus.Down) {
			return Object.values(downSystems) ?? []
		}
		return Object.values(pausedSystems) ?? []
	}, [data, statusFilter])

	const orderedData = useMemo(() => orderSystems(filteredData, systemOrder), [filteredData, systemOrder])

	const [viewMode, setViewMode] = useState<ViewMode>(
		() =>
			$userSettings.get().viewMode ??
			(JSON.parse(localStorage.getItem("besz-viewMode") || "null") as ViewMode | null) ??
			// show grid view on mobile if there are less than 200 systems (looks better but table is more efficient)
			(window.innerWidth < 1024 && filteredData.length < 200 ? "grid" : "table")
	)

	useEffect(() => {
		if (filter !== undefined) {
			table.getColumn("system")?.setFilterValue(filter)
		}
	}, [filter])

	const columnDefs = useMemo(() => SystemsTableColumns(viewMode), [viewMode])

	const table = useReactTable({
		data: orderedData,
		columns: columnDefs,
		getCoreRowModel: getCoreRowModel(),
		onSortingChange: handleSortingChange,
		getSortedRowModel: getSortedRowModel(),
		onColumnFiltersChange: setColumnFilters,
		getFilteredRowModel: getFilteredRowModel(),
		onColumnVisibilityChange: handleColumnVisibilityChange,
		state: {
			sorting,
			columnFilters,
			columnVisibility,
		},
		defaultColumn: {
			// header clicks go ascending, descending, then unsorted, which shows the saved drag order
			sortDescFirst: false,
			invertSorting: true,
			sortUndefined: "last",
			minSize: 0,
			size: 900,
			maxSize: 900,
		},
	})

	const rows = table.getRowModel().rows
	const rowIds = useMemo(() => rows.map((row) => row.original.id), [rows])
	// an empty sort leaves systems in the user's custom order
	const customOrder = sorting.length === 0

	const sensors = useSensors(
		useSensor(PointerSensor, pointerSensorOptions),
		useSensor(KeyboardSensor, keyboardSensorOptions)
	)

	const handleDragEnd = useCallback(
		({ active, over }: DragEndEvent) => {
			if (!over || active.id === over.id) {
				return
			}
			// the order covers every system, so a drag in a filtered view moves it within the full list
			let order = orderSystems($systems.get(), systemOrder).map((system) => system.id)
			if (!customOrder) {
				// dropping while sorted by a column switches to custom, starting from the order on screen
				order = seedOrder(rowIds, order)
				handleSortingChange([])
			}
			const next = moveSystem(order, String(active.id), String(over.id))
			setSystemOrder(next)
			localStorage.setItem("besz-systemOrder", JSON.stringify(next))
			$userSettings.setKey("systemOrder", next)
			queueUserSettings({ systemOrder: next })
		},
		[systemOrder, customOrder, rowIds]
	)

	const columns = table.getAllColumns()
	const visibleColumns = table.getVisibleLeafColumns()

	const [upSystemsLength, downSystemsLength, pausedSystemsLength] = useMemo(() => {
		return [Object.values(upSystems).length, Object.values(downSystems).length, Object.values(pausedSystems).length]
	}, [upSystems, downSystems, pausedSystems])

	const CardHead = useMemo(() => {
		return (
			<CardHeader className="p-0 mb-3 sm:mb-4">
				<div className="grid md:flex gap-x-5 gap-y-3 w-full items-end">
					<div className="px-2 sm:px-1">
						<CardTitle className="mb-2">
							<Trans>All Systems</Trans>
						</CardTitle>
						<CardDescription className="flex">
							<Trans>Click on a system to view more information.</Trans>
						</CardDescription>
					</div>

					<div className="flex gap-2 ms-auto w-full md:w-80">
						<div className="relative flex-1">
							<Input
								placeholder={t`Filter...`}
								onChange={(e) => setFilter(e.target.value)}
								value={filter}
								className="ps-4 pe-10 w-full"
							/>
							{filter && (
								<Button
									type="button"
									variant="ghost"
									size="icon"
									aria-label={t`Clear`}
									className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 text-muted-foreground"
									onClick={() => setFilter("")}
								>
									<XIcon className="h-4 w-4" />
								</Button>
							)}
						</div>
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button aria-label={t`View`} variant="outline" size="icon">
									<Settings2Icon className="size-4 opacity-80" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end" className="h-72 md:h-auto min-w-48 md:min-w-auto overflow-y-auto">
								<div className="grid grid-cols-1 md:grid-cols-4 divide-y md:divide-s md:divide-y-0">
									<div className="border-r">
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<LayoutGridIcon className="size-4" />
											<Trans>Layout</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<DropdownMenuRadioGroup className="px-1 pb-1" value={viewMode} onValueChange={handleViewModeChange}>
											<DropdownMenuRadioItem value="table" onSelect={(e) => e.preventDefault()} className="gap-2">
												<LayoutListIcon className="size-4" />
												<Trans>Table</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="grid" onSelect={(e) => e.preventDefault()} className="gap-2">
												<LayoutGridIcon className="size-4" />
												<Trans>Grid</Trans>
											</DropdownMenuRadioItem>
										</DropdownMenuRadioGroup>
									</div>

									<div className="border-r">
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<FilterIcon className="size-4" />
											<Trans>Status</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<DropdownMenuRadioGroup
											className="px-1 pb-1"
											value={statusFilter}
											onValueChange={handleStatusFilterChange}
										>
											<DropdownMenuRadioItem value="all" onSelect={(e) => e.preventDefault()}>
												<Trans>All Systems</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="up" onSelect={(e) => e.preventDefault()}>
												<Trans>Up ({upSystemsLength})</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="down" onSelect={(e) => e.preventDefault()}>
												<Trans>Down ({downSystemsLength})</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="paused" onSelect={(e) => e.preventDefault()}>
												<Trans>Paused ({pausedSystemsLength})</Trans>
											</DropdownMenuRadioItem>
										</DropdownMenuRadioGroup>
									</div>

									<div className="border-r">
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<ArrowUpDownIcon className="size-4" />
											<Trans>Sort By</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<div className="px-1 pb-1">
											<DropdownMenuItem
												onSelect={(e) => {
													e.preventDefault()
													handleSortingChange([])
												}}
											>
												{customOrder ? <GripVerticalIcon className="me-2 size-4" /> : <span className="w-6"></span>}
												<Trans>Custom</Trans>
											</DropdownMenuItem>
											{columns.map((column) => {
												if (!column.getCanSort()) return null
												let Icon = <span className="w-6"></span>
												// if current sort column, show sort direction
												if (sorting[0]?.id === column.id) {
													if (sorting[0]?.desc) {
														Icon = <ArrowUpIcon className="me-2 size-4" />
													} else {
														Icon = <ArrowDownIcon className="me-2 size-4" />
													}
												}
												return (
													<DropdownMenuItem
														onSelect={(e) => {
															e.preventDefault()
															handleSortingChange([
																{ id: column.id, desc: sorting[0]?.id === column.id && !sorting[0]?.desc },
															])
														}}
														key={column.id}
													>
														{Icon}
														{/* @ts-ignore */}
														{column.columnDef.name()}
													</DropdownMenuItem>
												)
											})}
										</div>
									</div>

									<div>
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<EyeIcon className="size-4" />
											<Trans>Visible Fields</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<div className="px-1.5 pb-1">
											{columns
												.filter((column) => column.getCanHide())
												.map((column) => {
													return (
														<DropdownMenuCheckboxItem
															key={column.id}
															onSelect={(e) => e.preventDefault()}
															checked={column.getIsVisible()}
															onCheckedChange={(value) => column.toggleVisibility(!!value)}
														>
															{/* @ts-ignore */}
															{column.columnDef.name()}
														</DropdownMenuCheckboxItem>
													)
												})}
										</div>
									</div>
								</div>
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				</div>
			</CardHeader>
		)
	}, [
		visibleColumns.length,
		sorting,
		viewMode,
		locale,
		statusFilter,
		upSystemsLength,
		downSystemsLength,
		pausedSystemsLength,
		filter,
	])

	return (
		<Card className="w-full px-3 py-5 sm:py-6 sm:px-6">
			{CardHead}
			<DndContext
				sensors={sensors}
				collisionDetection={closestCenter}
				modifiers={viewMode === "table" ? tableModifiers : gridModifiers}
				onDragEnd={handleDragEnd}
			>
				<SortableContext
					items={rowIds}
					strategy={viewMode === "table" ? verticalListSortingStrategy : rectSortingStrategy}
				>
					{viewMode === "table" ? (
						// table layout
						<div className="rounded-md">
							<AllSystemsTable table={table} rows={rows} colLength={visibleColumns.length} isLoading={!systemsLoaded} />
						</div>
					) : (
						// grid layout
						<div className="grid gap-4 grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
							{rows?.length ? (
								rows.map((row) => {
									return <SystemCard key={row.original.id} row={row} table={table} colLength={visibleColumns.length} />
								})
							) : (
								<div className="col-span-full text-center py-8">
									{systemsLoaded ? (
										<Trans>No systems found.</Trans>
									) : (
										<LoaderCircleIcon className="animate-spin size-10 opacity-60 mx-auto" />
									)}
								</div>
							)}
						</div>
					)}
				</SortableContext>
			</DndContext>
		</Card>
	)
}

const AllSystemsTable = memo(
	({
		table,
		rows,
		colLength,
		isLoading,
	}: {
		table: TableType<SystemRecord>
		rows: Row<SystemRecord>[]
		colLength: number
		isLoading: boolean
	}) => {
		// The virtualizer will need a reference to the scrollable container element
		const scrollRef = useRef<HTMLDivElement>(null)

		const { active } = useDndContext()
		const activeIndex = active ? rows.findIndex((row) => row.original.id === active.id) : -1
		// keep the dragged row mounted when it scrolls out of view. The rows in between stay too, because
		// the spacer padding below assumes the rendered rows are contiguous
		const rangeExtractor = useCallback(
			(range: Range) =>
				defaultRangeExtractor(
					activeIndex < 0
						? range
						: {
								...range,
								startIndex: Math.min(range.startIndex, activeIndex),
								endIndex: Math.max(range.endIndex, activeIndex),
							}
				),
			[activeIndex]
		)

		const virtualizer = useVirtualizer<HTMLDivElement, HTMLTableRowElement>({
			count: rows.length,
			estimateSize: () => (rows.length > 10 ? 56 : 60),
			getScrollElement: () => scrollRef.current,
			overscan: 5,
			rangeExtractor,
		})
		const virtualRows = virtualizer.getVirtualItems()

		const paddingTop = Math.max(0, virtualRows[0]?.start ?? 0 - virtualizer.options.scrollMargin)
		const paddingBottom = Math.max(0, virtualizer.getTotalSize() - (virtualRows[virtualRows.length - 1]?.end ?? 0))

		return (
			<div
				className={cn(
					"h-min max-h-[calc(100dvh-17rem)] max-w-full relative overflow-auto border rounded-md",
					// don't set min height if there are less than 2 rows, do set if we need to display the empty state
					(!rows.length || rows.length > 2) && "min-h-50"
				)}
				ref={scrollRef}
			>
				{/* add header height to table size */}
				<div style={{ height: `${virtualizer.getTotalSize() + 50}px`, paddingTop, paddingBottom }}>
					<table className="text-sm w-full h-full">
						<SystemsTableHead table={table} />
						<TableBody onMouseEnter={preloadSystemDetail}>
							{rows.length ? (
								virtualRows.map((virtualRow) => {
									const row = rows[virtualRow.index] as Row<SystemRecord>
									return (
										<SystemTableRow
											key={row.original.id}
											row={row}
											virtualRow={virtualRow}
											length={rows.length}
											colLength={colLength}
										/>
									)
								})
							) : (
								<TableRow>
									<TableCell colSpan={colLength} className="h-37 text-center pointer-events-none">
										{isLoading ? (
											<LoaderCircleIcon className="animate-spin size-10 opacity-60 mx-auto" />
										) : (
											<Trans>No systems found.</Trans>
										)}
									</TableCell>
								</TableRow>
							)}
						</TableBody>
					</table>
				</div>
			</div>
		)
	}
)

function SystemsTableHead({ table }: { table: TableType<SystemRecord> }) {
	useLinguiContext()
	return (
		<TableHeader className="sticky top-0 z-50 w-full border-b-2">
			{table.getHeaderGroups().map((headerGroup) => (
				<tr key={headerGroup.id}>
					{headerGroup.headers.map((header) => {
						return (
							<TableHead className="px-1.5" key={header.id}>
								{flexRender(header.column.columnDef.header, header.getContext())}
							</TableHead>
						)
					})}
				</tr>
			))}
		</TableHeader>
	)
}

type SortableBindings = Pick<ReturnType<typeof useSortable>, "attributes" | "listeners" | "setActivatorNodeRef">

function DragHandle({ attributes, listeners, setActivatorNodeRef }: SortableBindings) {
	const { t } = useLingui()
	return (
		<Button
			ref={setActivatorNodeRef}
			variant="ghost"
			size="icon"
			aria-label={t`Drag to reorder`}
			className="relative z-10 size-7 shrink-0 cursor-grab touch-none text-muted-foreground active:cursor-grabbing"
			{...attributes}
			{...listeners}
		>
			<GripVerticalIcon className="size-4" />
		</Button>
	)
}

const SystemTableRow = memo(
	({
		row,
		virtualRow,
		colLength,
	}: {
		row: Row<SystemRecord>
		virtualRow: VirtualItem
		length: number
		colLength: number
	}) => {
		const system = row.original
		const { t } = useLingui()
		const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
			id: system.id,
		})
		const translate = CSS.Translate.toString(transform)
		return useMemo(() => {
			return (
				<TableRow
					ref={setNodeRef}
					style={{ transform: translate, transition }}
					// data-state={row.getIsSelected() && "selected"}
					className={cn("cursor-pointer transition-opacity relative safari:transform-3d", {
						"opacity-50": system.status === SystemStatus.Paused,
						"z-20 bg-muted shadow-md": isDragging,
					})}
				>
					{row.getVisibleCells().map((cell) => {
						const content = flexRender(cell.column.columnDef.cell, cell.getContext())
						return (
							<TableCell
								key={cell.id}
								style={{
									width: cell.column.getSize(),
									height: virtualRow.size,
								}}
								className="py-0 ps-4.5"
							>
								{cell.column.id === "system" ? (
									<div className="flex items-center gap-1">
										<DragHandle
											attributes={attributes}
											listeners={listeners}
											setActivatorNodeRef={setActivatorNodeRef}
										/>
										{content}
									</div>
								) : (
									content
								)}
							</TableCell>
						)
					})}
				</TableRow>
			)
		}, [system, system.status, colLength, t, isDragging, translate, transition, attributes, listeners])
	}
)

const SystemCard = memo(
	({ row, table, colLength }: { row: Row<SystemRecord>; table: TableType<SystemRecord>; colLength: number }) => {
		const system = row.original
		const { t } = useLingui()
		const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
			id: system.id,
		})
		const translate = CSS.Translate.toString(transform)

		return useMemo(() => {
			return (
				<Card
					ref={setNodeRef}
					style={{ transform: translate, transition: (!isDragging && transition) || cardTransition }}
					onMouseEnter={preloadSystemDetail}
					key={system.id}
					className={cn(
						"cursor-pointer hover:shadow-md transition-all bg-transparent w-full dark:border-border duration-200 relative",
						{
							"opacity-50": system.status === SystemStatus.Paused,
							"z-20 shadow-lg": isDragging,
						}
					)}
				>
					<CardHeader className="py-1 ps-4 pe-2 bg-muted/30 border-b border-border/60">
						<div className="flex items-center gap-1 w-full overflow-hidden">
							<h3 className="text-primary/90 min-w-0 flex-1 gap-2.5 font-semibold">
								<div className="flex items-center gap-2.5 min-w-0 flex-1">
									<DragHandle attributes={attributes} listeners={listeners} setActivatorNodeRef={setActivatorNodeRef} />
									<IndicatorDot system={system} />
									<span className="text-[.95em]/normal tracking-normal text-primary/90 truncate">{system.name}</span>
								</div>
							</h3>
							{table.getColumn("actions")?.getIsVisible() && (
								<div className="flex gap-1 shrink-0 relative z-10">
									<AlertButton system={system} />
									<ActionsButton system={system} />
								</div>
							)}
						</div>
					</CardHeader>
					<CardContent className="text-sm px-5 pt-3.5 pb-4">
						<div className="grid gap-2.5" style={{ gridTemplateColumns: "24px minmax(80px, max-content) minmax(0, 1fr)" }}>
							{table.getAllColumns().map((column) => {
								if (!column.getIsVisible() || column.id === "system" || column.id === "actions") return null
								const cell = row.getAllCells().find((cell) => cell.column.id === column.id)
								if (!cell) return null
								// @ts-expect-error
								const { Icon, name } = column.columnDef as ColumnDef<SystemRecord, unknown>
								return (
									<>
										<div key={`${column.id}-icon`} className="flex items-center">
											{column.id === "lastSeen" ? (
												<EyeIcon className="size-4 text-muted-foreground" />
											) : (
												Icon && <Icon className="size-4 text-muted-foreground" />
											)}
										</div>
										<div key={`${column.id}-label`} className="flex items-center text-muted-foreground pr-3">
											{name()}:
										</div>
										<div key={`${column.id}-value`} className="flex items-center min-w-0">
											{flexRender(cell.column.columnDef.cell, cell.getContext())}
										</div>
									</>
								)
							})}
						</div>
					</CardContent>
					<Link
						href={getPagePath($router, "system", { id: row.original.id })}
						className="inset-0 absolute w-full h-full"
					>
						<span className="sr-only">{row.original.name}</span>
					</Link>
				</Card>
			)
		}, [system, colLength, t, isDragging, translate, transition, attributes, listeners])
	}
)
