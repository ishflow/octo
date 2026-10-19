import Foundation
import SwiftUI
import Testing

@Suite struct TableSortTests {
    @Test func roundTripKeepsColumnAndDirection() {
        let saved = TableSort.encode([KeyPathComparator(\Item.size, order: .reverse), KeyPathComparator(\Item.name)])
        #expect(saved == "size-,name+")
        let back = TableSort.decode(saved)
        #expect(back.count == 2)
        #expect(back[0].keyPath == \Item.size as PartialKeyPath<Item>)
        #expect(back[0].order == .reverse)
        #expect(back[1].keyPath == \Item.name as PartialKeyPath<Item>)
        #expect(back[1].order == .forward)
    }

    @Test func everyTableColumnHasASavedName() {
        let paths: [PartialKeyPath<Item>] = [\Item.name, \Item.size, \Item.stateSort, \Item.eta, \Item.speed, \Item.lastTrySort, \Item.description]
        for p in paths { #expect(TableSort.keyName(p) != nil, "no saved name for \(p)") }
    }

    @Test func emptyOrGarbageFallsBackToOrderOfAddition() {
        for s in ["", "nope+", "size", "size*", ",,"] {
            let d = TableSort.decode(s)
            #expect(d.count == 1 && d[0].keyPath == \Item.added as PartialKeyPath<Item> && d[0].order == .forward, "input \(s)")
        }
    }

    @Test func unknownPartIsDroppedKnownPartKept() {
        let d = TableSort.decode("bogus+,eta-")
        #expect(d.count == 1)
        #expect(d[0].keyPath == \Item.eta as PartialKeyPath<Item>)
        #expect(d[0].order == .reverse)
    }
}

@Suite struct DropRoutingTests {
    private func drop(_ s: String) -> [DroppedLinks] { [DroppedLinks(text: s)] }

    @Test func oneLinkGoesToTheAddSheet() {
        var single: [String] = [], many: [[String]] = []
        let ok = DropLinks.handle(drop("https://x.com/a.zip"), single: { single.append($0) }, multiple: { many.append($0) })
        #expect(ok && single == ["https://x.com/a.zip"] && many.isEmpty)
    }

    @Test func severalLinksGoToTheImportList() {
        var single: [String] = [], many: [[String]] = []
        let ok = DropLinks.handle(drop("https://x.com/a.zip\nhttps://x.com/b.zip"), single: { single.append($0) }, multiple: { many.append($0) })
        #expect(ok && single.isEmpty && many == [["https://x.com/a.zip", "https://x.com/b.zip"]])
    }

    @Test func noLinkRejectsTheDrop() {
        let ok = DropLinks.handle(drop("merhaba"), single: { _ in Issue.record("single") }, multiple: { _ in Issue.record("multiple") })
        #expect(!ok)
    }
}
