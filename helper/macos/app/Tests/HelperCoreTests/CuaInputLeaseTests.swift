import XCTest
@testable import HelperCore

final class CuaInputLeaseTests: XCTestCase {
    func testGrantExtendBusyReleaseAndExpiry() throws {
        let clock = Locked<TimeInterval>(100)
        let lease = CuaInputLease(now: { clock.withLock { $0 } })
        XCTAssertEqual(lease.status(), .null)
        XCTAssertEqual(try lease.grant(holder: "hermes-a"), ["holder": .string("hermes-a"), "expires_in_s": .number(300)])
        clock.withLock { $0 += 50 }
        XCTAssertEqual(lease.status().object?["expires_in_s"], .number(250))
        XCTAssertThrowsError(try lease.grant(holder: "hermes-b")) {
            XCTAssertEqual(($0 as? HelperError)?.code, .busy)
            XCTAssertEqual(($0 as? HelperError)?.detail, "hermes-a is using this Mac (250s left)")
        }
        XCTAssertThrowsError(try lease.release(holder: "hermes-b")) {
            XCTAssertEqual(($0 as? HelperError)?.code, .busy)
        }
        XCTAssertEqual(try lease.grant(holder: "hermes-a")["expires_in_s"], .number(300))
        XCTAssertEqual(lease.status().object?["expires_in_s"], .number(300))
        XCTAssertEqual(try lease.grant(holder: "hermes-a", ttl: 20)["expires_in_s"], .number(20))
        clock.withLock { $0 += 19.5 }
        XCTAssertEqual(lease.status().object?["expires_in_s"], .number(1))
        clock.withLock { $0 += 0.5 }
        XCTAssertEqual(lease.status(), .null)
        _ = try lease.grant(holder: "hermes-b", ttl: 1)
        clock.withLock { $0 += 1 }
        // Release by a different identity is allowed once the lease has expired.
        try lease.release(holder: "hermes-a")
        XCTAssertEqual(lease.status(), .null)
        _ = try lease.grant(holder: "hermes-a", ttl: 1)
        clock.withLock { $0 += 1 }
        XCTAssertEqual(try lease.grant(holder: "hermes-b")["holder"], .string("hermes-b"))
        try lease.release(holder: "hermes-b")
        try lease.release(holder: "hermes-a") // No lease is also a successful release.
        XCTAssertEqual(lease.status(), .null)
    }

    func testHolderAndTTLValidation() throws {
        let lease = CuaInputLease()
        for holder in ["a", "A_z-0.9:@", String(repeating: "a", count: 64)] {
            _ = try lease.grant(holder: holder, ttl: 0.5)
            try lease.release(holder: holder)
        }
        for holder in ["", "a b", "a\n", "é", "🙂", "a/b", String(repeating: "a", count: 65)] {
            for action in [{ _ = try lease.grant(holder: holder) }, { try lease.release(holder: holder) }] {
                XCTAssertThrowsError(try action()) { XCTAssertEqual(($0 as? HelperError)?.code, .badRequest) }
            }
        }
        for ttl in [0, -1, 300.01, .infinity, -.infinity, .nan] {
            XCTAssertThrowsError(try lease.grant(holder: "valid", ttl: ttl)) {
                XCTAssertEqual(($0 as? HelperError)?.code, .badRequest)
            }
        }
        XCTAssertEqual(lease.status(), .null)
    }

    func testConcurrentGrantsHaveOneWinner() {
        let lease = CuaInputLease()
        let winners = Locked<[String]>([])
        DispatchQueue.concurrentPerform(iterations: 32) { index in
            let holder = "hermes-\(index)"
            do {
                _ = try lease.grant(holder: holder)
                winners.withLock { $0.append(holder) }
            } catch { XCTAssertEqual((error as? HelperError)?.code, .busy) }
        }
        XCTAssertEqual(winners.withLock { $0.count }, 1)
        XCTAssertEqual(lease.status().object?["holder"]?.string, winners.withLock { $0.first })
    }
}
