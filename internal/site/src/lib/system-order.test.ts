import { expect, test } from "bun:test"
import { moveSystem, orderSystems } from "./system-order"

const sys = (id: string, name: string) => ({ id, name })
const ids = (systems: { id: string }[]) => systems.map((s) => s.id)

const fringe = sys("f", "Fringe")
const centralC = sys("c", "Central C")
const centralB = sys("b", "Central B")
const centralA = sys("a", "Central A")
const beacon = sys("x", "Beacon")
const all = [fringe, centralC, centralB, centralA, beacon]

test("systems follow the saved order", () => {
	expect(ids(orderSystems(all, ["x", "a", "b", "c", "f"]))).toEqual(["x", "a", "b", "c", "f"])
})

test("systems missing from the saved order come last, sorted by name", () => {
	expect(ids(orderSystems(all, ["f", "c"]))).toEqual(["f", "c", "x", "a", "b"])
})

test("without a saved order systems are sorted by name", () => {
	expect(ids(orderSystems(all, []))).toEqual(["x", "a", "b", "c", "f"])
	expect(ids(orderSystems(all, undefined))).toEqual(["x", "a", "b", "c", "f"])
})

test("ids of deleted systems in the saved order are ignored", () => {
	expect(ids(orderSystems([fringe, beacon], ["gone", "x", "also-gone", "f"]))).toEqual(["x", "f"])
})

test("a repeated id in the saved order keeps its first position", () => {
	expect(ids(orderSystems([fringe, beacon, centralA], ["f", "x", "f", "a"]))).toEqual(["f", "x", "a"])
})

test("a filtered subset keeps its relative order from the full saved order", () => {
	const order = ["b", "x", "f", "a", "c"]
	expect(ids(orderSystems([fringe, centralA, beacon], order))).toEqual(["x", "f", "a"])
})

test("ordering returns a new array and leaves the input untouched", () => {
	const input = [...all]
	const result = orderSystems(input, ["x", "a"])
	expect(result).not.toBe(input)
	expect(input).toEqual(all)
})

test("moving down places the system right after the target", () => {
	expect(moveSystem(["a", "b", "c", "d"], "a", "c")).toEqual(["b", "c", "a", "d"])
})

test("moving up places the system right before the target", () => {
	expect(moveSystem(["a", "b", "c", "d"], "d", "b")).toEqual(["a", "d", "b", "c"])
})

test("moving to the ends of the list", () => {
	expect(moveSystem(["a", "b", "c"], "a", "c")).toEqual(["b", "c", "a"])
	expect(moveSystem(["a", "b", "c"], "c", "a")).toEqual(["c", "a", "b"])
})

test("a drag inside a filtered subset lands correctly in the full order", () => {
	const full = ["a", "b", "c", "d", "e"]
	// filter shows a, c, e. Dragging a over e puts it after e, and the subset reads c, e, a
	const down = moveSystem(full, "a", "e")
	expect(down).toEqual(["b", "c", "d", "e", "a"])
	expect(down.filter((id) => ["a", "c", "e"].includes(id))).toEqual(["c", "e", "a"])
	// dragging e over c puts it before c, and the subset reads a, e, c
	const up = moveSystem(full, "e", "c")
	expect(up).toEqual(["a", "b", "e", "c", "d"])
	expect(up.filter((id) => ["a", "c", "e"].includes(id))).toEqual(["a", "e", "c"])
})

test("moving a system onto itself changes nothing", () => {
	expect(moveSystem(["a", "b", "c"], "b", "b")).toEqual(["a", "b", "c"])
})

test("unknown ids change nothing", () => {
	expect(moveSystem(["a", "b", "c"], "nope", "b")).toEqual(["a", "b", "c"])
	expect(moveSystem(["a", "b", "c"], "a", "nope")).toEqual(["a", "b", "c"])
	expect(moveSystem([], "a", "b")).toEqual([])
})

test("moving returns a new array and leaves the input untouched", () => {
	const input = ["a", "b", "c"]
	const result = moveSystem(input, "a", "c")
	expect(result).not.toBe(input)
	expect(input).toEqual(["a", "b", "c"])
	expect(moveSystem(input, "a", "a")).not.toBe(input)
})

test("moving then ordering reproduces the moved order", () => {
	const moved = moveSystem(ids(orderSystems(all, [])), "x", "f")
	expect(moved).toEqual(["a", "b", "c", "f", "x"])
	expect(ids(orderSystems(all, moved))).toEqual(moved)
})
