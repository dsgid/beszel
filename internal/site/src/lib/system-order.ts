interface Orderable {
	id: string
	name: string
}

/**
 * Sorts systems by a user-defined order of ids. Systems missing from the order (added since it was
 * saved) come last, sorted by name. Ids in the order that no longer exist are ignored.
 * Returns a new array.
 */
export function orderSystems<T extends Orderable>(systems: readonly T[], order: readonly string[] = []): T[] {
	const rank = new Map<string, number>()
	order.forEach((id, i) => {
		if (!rank.has(id)) {
			rank.set(id, i)
		}
	})
	return [...systems].sort((a, b) => {
		const rankA = rank.get(a.id)
		const rankB = rank.get(b.id)
		if (rankA !== undefined && rankB !== undefined) {
			return rankA - rankB
		}
		if (rankA !== undefined) {
			return -1
		}
		if (rankB !== undefined) {
			return 1
		}
		return a.name.localeCompare(b.name)
	})
}

/**
 * Moves `activeId` to the position of `overId` in the complete list of ids, so a drag within a
 * filtered subset lands correctly in the full order: moving down places the system right after
 * `overId`, moving up right before it. Returns a new array, unchanged when either id is unknown.
 */
export function moveSystem(fullOrderIds: readonly string[], activeId: string, overId: string): string[] {
	const next = [...fullOrderIds]
	const from = next.indexOf(activeId)
	const to = next.indexOf(overId)
	if (from === -1 || to === -1 || from === to) {
		return next
	}
	const [moved] = next.splice(from, 1)
	next.splice(to, 0, moved)
	return next
}

/**
 * Starting point for a custom order when the user drags while sorted by a column: the systems on
 * screen in their displayed order, then every other system in its existing order. Returns a new array.
 */
export function seedOrder(displayedIds: readonly string[], fullOrderIds: readonly string[]): string[] {
	const displayed = new Set(displayedIds)
	return [...displayedIds, ...fullOrderIds.filter((id) => !displayed.has(id))]
}
