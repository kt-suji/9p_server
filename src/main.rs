// tokioのマクロを使って、非同期なmain関数を動かす準備
#[tokio::main]
async fn main() {
    println!("[INFO] 9P Server プロジェクト起動中...");
    // 非同期なスリープ(1秒待つ)
    tokio::time::sleep(tokio::time::Duration::from_secs(1)).await;
    println!("[INFO] 準備完了");
}
