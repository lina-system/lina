// These are compiled into signed code, never loaded from user-writable bundle resources.
// CuaDriverTests verifies agreement with app/cua-driver.pin.
public enum CuaDriverPin {
    public static let version = "0.38.0"
    static let identifier = "cua-driver"
    static let team = "YCK386LBJ7"
    static let cdhash = "b067c86ef1ca3e63a8c86eba9fe29d61bd00ad68"
    static let requirement = "identifier \"\(identifier)\" and anchor apple generic and certificate leaf[subject.OU] = \"\(team)\""
}
